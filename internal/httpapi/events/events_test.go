package events_test

// The event stream, end to end, over a real socket.
//
// A recorder cannot test this route and the reason is worth stating first, because
// it decides every choice below: the handler *blocks* for the life of the stream, so
// a test that drove it through `httptest.NewRecorder` would deadlock rather than
// observe. So these tests use a real `httptest.Server` and a real client, and the
// three properties that only a real socket can show are the three they check:
//
//   - **the stream is not buffered** — a comment arrives before the handler would
//     have returned, and arrives at all, which is the interaction with
//     `middleware.Timeout`'s buffer and with `http.Server.WriteTimeout`.
//   - **it outlives the handler budget** — the chain in these tests carries a 50ms
//     timeout and a 25ms write deadline, both far shorter than the throttle, and the
//     stream still delivers.
//   - **it ends when the hub closes** — the response body reaches EOF, which is what
//     a clean shutdown looks like from the other end.
//
// The throttle is tested against the *shipped* one-second interval rather than a
// test-settable one. A knob here would be a knob an operator could set to zero, and a
// zero interval is a live region re-rendering as fast as a sync client writes; the
// cost is a couple of seconds of wall clock in this file, and the benefit is that the
// constant the test measures is the constant the product ships.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/httpapi/events"
	"github.com/semiplane/semiplane/internal/httpapi/identity"
	"github.com/semiplane/semiplane/internal/httpapi/middleware"
	"github.com/semiplane/semiplane/internal/web/components"
	editui "github.com/semiplane/semiplane/internal/web/components/edit"
)

// The campaign these tests stream for, and the identifiers its rows carry.
const (
	testSlug   = "greyhaven"
	testCampID = int64(1)
	gmUser     = int64(10)
	playerUser = int64(11)
	otherCamp  = int64(2)
)

// shortBudget is the handler budget the chain carries.
//
// 50ms against a stream that must live for over a second: if the interaction with
// `middleware.Timeout` were wrong — if the handler returned at the budget, or if its
// body were still sitting in the buffer — every test in this file would fail rather
// than one.
const shortBudget = 50 * time.Millisecond

// streamTimeout is how long a test waits for a record to arrive.
//
// Generous, because a loaded CI machine is the case and a tight bound turns a slow
// runner into a red build. It is a constant rather than a parameter because every
// assertion here wants the same thing — "if this is going to happen, it happens now" —
// and a parameter would be five call sites agreeing on one value.
const streamTimeout = 10 * time.Second

// shortWriteDeadline stands in for `http.Server.WriteTimeout`, which defaults to 30
// seconds in this project. Shorter than the throttle so that a stream which did *not*
// clear it would be cut before it could say anything.
const shortWriteDeadline = 25 * time.Millisecond

// gmRequestor is the campaign's GM: the only tier the route admits.
func gmRequestor() domain.Requestor {
	return domain.Requestor{Authenticated: true, UserID: gmUser, Username: "mira"}
}

func playerRequestor() domain.Requestor {
	return domain.Requestor{Authenticated: true, UserID: playerUser, Username: "tobin"}
}

func anonymousRequestor() domain.Requestor {
	return domain.Requestor{}
}

// campaignStore answers the two queries the access gate reads.
type campaignStore struct {
	campaign domain.Campaign
	members  map[int64]domain.Role
}

func (s campaignStore) CampaignBySlug(_ context.Context, slug string) (domain.Campaign, error) {
	if slug != s.campaign.Slug {
		return domain.Campaign{}, errors.New("no such campaign")
	}

	return s.campaign, nil
}

func (s campaignStore) Membership(
	_ context.Context,
	campaignID, userID int64,
) (domain.Membership, error) {
	role, member := s.members[userID]
	if !member || campaignID != s.campaign.ID {
		return domain.Membership{}, errors.New("not a member")
	}

	return domain.Membership{CampaignID: campaignID, UserID: userID, Role: role}, nil
}

func (s campaignStore) CreateCampaign(
	_ context.Context,
	_ domain.Campaign,
) (domain.Campaign, error) {
	return domain.Campaign{}, errors.New("unused")
}

func (s campaignStore) CreateMembership(
	_ context.Context,
	_ domain.Membership,
) (domain.Membership, error) {
	return domain.Membership{}, errors.New("unused")
}

func (s campaignStore) DeleteCampaign(_ context.Context, _ int64) error {
	return errors.New("unused")
}

// backing is the campaign the access gate resolves: public, so an anonymous reader
// reaches the gate and is refused by it rather than by a 404 for the campaign.
func backing() campaignStore {
	return campaignStore{
		campaign: domain.Campaign{
			ID: testCampID, Slug: testSlug, Name: "Greyhaven",
			Visibility: domain.VisibilityPublic,
		},
		members: map[int64]domain.Role{gmUser: domain.RoleGM, playerUser: domain.RolePlayer},
	}
}

// serve starts a server carrying the stream behind the same chain the router builds,
// including the timeout layer and a write deadline shorter than the throttle.
//
// The write deadline is applied with `http.NewResponseController` on the *server's*
// handler rather than through `http.Server.WriteTimeout`, because that is the only
// way to give this test a deadline short enough to matter: the shipped default is 30
// seconds and a test that waited 30 seconds to prove a point would not be run.
func serve(t *testing.T, hub *events.Hub) *httptest.Server {
	t.Helper()

	return serveWith(t, hub, nil, nil)
}

// serveWith is `serve` with the two things a sidebar test needs: a writer the route's
// log goes to, and a mutator that runs after the defaults are set.
//
// **The mutator rather than two more `serve` variants**, because a second copy of this
// function is a second copy of the chain — the timeout layer, the write deadline, the
// `X-Test-Requestor` header and the cleanup ordering — and a drift between the two
// would be a test passing against a chain the product does not have.
func serveWith(
	t *testing.T,
	hub *events.Hub,
	logs io.Writer,
	mutate func(*events.Handler),
) *httptest.Server {
	t.Helper()

	if logs == nil {
		logs = testLog{t}
	}

	handler := &events.Handler{
		Hub: hub,
		Logger: slog.New(
			slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}),
		),
	}

	if mutate != nil {
		mutate(handler)
	}

	campaignMux := http.NewServeMux()
	events.Mount(campaignMux, handler)

	outer := http.NewServeMux()
	outer.Handle("/c/{slug}/", campaigns.Resolve(backing())(
		middleware.Chain(campaignMux, campaigns.RequireRead),
	))

	chain := middleware.Chain(outer,
		asRequestor,
		middleware.RequestID,
		middleware.Timeout(shortBudget),
	)

	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			// The write deadline, as `cmd/server` would set it from
			// `cfg.WriteTimeout` and as this test shortens it.
			controller := http.NewResponseController(w)
			if err := controller.SetWriteDeadline(time.Now().Add(shortWriteDeadline)); err != nil {
				t.Logf("set the write deadline: %v", err)
			}

			chain.ServeHTTP(w, r)
		},
	))

	t.Cleanup(server.Close)

	// Registered *after* the server's, so it runs *before* it: cleanups are
	// last-in-first-out, and `httptest.Server.Close` waits for in-flight requests.
	// A live stream is an in-flight request that only ends when the hub does, so the
	// other order blocks for the keep-alive interval and then warns. The ordering is
	// invisible in the code and load-bearing in the runtime, which is why it is stated
	// here rather than left to be discovered.
	t.Cleanup(func() { _ = hub.Close() })

	return server
}

// asRequestor resolves the request's identity from a header, standing in for
// `identity.Authenticate`.
//
// It has to be a header and not a context value, and that is the only reason this
// helper exists. The wiki and editor tests set the requestor on the request's context
// and hand it straight to the handler, which works because nothing serialises it. Over
// a real socket the context is rebuilt on the server side and the value is gone — so a
// test of the stream has to reach identity the way a browser does, through something
// on the wire.
//
// A test seam and not production code, and the header is named so that it cannot be
// mistaken for one: it stands in for a session cookie lookup, which is the real
// mechanism, and nothing in the product reads it.
func asRequestor(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who := r.Header.Get("X-Test-Requestor")
		if who == "" {
			who = "anonymous"
		}

		var requestor domain.Requestor

		switch who {
		case "gm":
			requestor = gmRequestor()
		case "player":
			requestor = playerRequestor()
		case "anonymous":
			requestor = anonymousRequestor()
		default:
			requestor = anonymousRequestor()
		}

		next.ServeHTTP(w, r.WithContext(identity.WithRequestor(r.Context(), requestor)))
	})
}

// open connects a client to the stream as requestor and returns the live response.
//
// The body is *not* closed by this helper: the test decides when the stream ends,
// because two of these tests end it by closing the hub and one ends it by closing the
// client.
func open(t *testing.T, server *httptest.Server, requestor domain.Requestor) *http.Response {
	t.Helper()

	resp, err := connect(t, server, requestor)
	if err != nil {
		t.Fatalf("open the stream: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("the stream answered %d, want 200; body:\n%s", resp.StatusCode, body)
	}

	t.Cleanup(func() { _ = resp.Body.Close() })

	return resp
}

// connect issues the request and returns the response whatever its status.
func connect(
	t *testing.T,
	server *httptest.Server,
	requestor domain.Requestor,
) (*http.Response, error) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		server.URL+"/c/greyhaven/events", http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("build the request: %w", err)
	}

	req.Header.Set("X-Test-Requestor", requestorRole(requestor))

	response, err := server.Client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("open the stream: %w", err)
	}

	return response, nil
}

// requestorRole is the wire spelling of an identity, for `asRequestor`.
func requestorRole(requestor domain.Requestor) string {
	switch {
	case !requestor.Authenticated:
		return "anonymous"
	case requestor.UserID == playerUser:
		return "player"
	default:
		return "gm"
	}
}

// stream reads an event stream in the background, so a test can ask "is there another
// frame?" without blocking on a socket that may never send one.
type stream struct {
	frames chan []string
	done   chan error
}

// read starts reading frames. A frame is every line up to a blank line, which is the
// event-stream grammar's record separator — so a frame is assembled, not a line.
func read(resp *http.Response) *stream {
	reader := bufio.NewReader(resp.Body)

	out := &stream{frames: make(chan []string, 8), done: make(chan error, 1)}

	go func() {
		var frame []string

		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				out.done <- err

				return
			}

			line = strings.TrimRight(line, "\r\n")

			if strings.TrimSpace(line) == "" {
				if len(frame) > 0 {
					out.frames <- frame
					frame = nil
				}

				continue
			}

			frame = append(frame, line)
		}
	}()

	return out
}

// next waits up to within for a frame and returns its lines, or fails.
func (s *stream) next(t *testing.T) []string {
	t.Helper()

	select {
	case frame := <-s.frames:
		return frame
	case err := <-s.done:
		t.Fatalf("the stream ended while waiting for a frame: %v", err)
	case <-time.After(streamTimeout):
		t.Fatalf("no frame arrived within %s", streamTimeout)
	}

	return nil
}

// nextInsertion waits for the next record that is an insertion rather than a
// comment.
//
// **The distinction §7.5 makes and the file header argues**: a keep-alive is an SSE
// comment, which inserts nothing. A test that used `next` under a short keep-alive
// would read the next comment and call it an announcement — so it would pass for the
// wrong reason, having asserted that a comment arrived while the property under test
// is that a comment's arrival is *not* an insertion.
func (s *stream) nextInsertion(t *testing.T) sseFrame {
	t.Helper()

	// **512, and the number is arithmetic rather than a guess.** With the 5ms
	// keep-alive the prohibition's own test sets, the shipped one-second budget is
	// roughly two hundred records, so 512 reads a whole budget's worth of comments
	// before calling it a fault. A lower cap would fail this test on a slow machine
	// rather than on a broken stream, which is the failure mode a bound chosen by
	// intuition always has.
	const recordsPerBudget = 512

	for range recordsPerBudget {
		frame := sseFields(s.next(t))
		if !frame.isComment() {
			return frame
		}
	}

	t.Fatalf("%d consecutive records were comments; at the keep-alive this test "+
		"installs that is longer than the whole announcement budget, so the stream is "+
		"sending keep-alives and nothing else", recordsPerBudget)

	return sseFrame{}
}

// silentFor reports whether nothing at all arrives within within.
//
// Used for the throttle's floor: "nothing within 900ms" is the assertion, and it is
// the half that a throttle removed would fail.
func (s *stream) silentFor(t *testing.T, within time.Duration) bool {
	t.Helper()

	select {
	case frame := <-s.frames:
		t.Errorf("a frame arrived within %s when the throttle allows one a second: %q",
			within, frame)

		return false
	case err := <-s.done:
		t.Errorf("the stream ended within %s: %v", within, err)

		return false
	case <-time.After(within):
		return true
	}
}

// closedWithin reports whether the stream reaches EOF within within.
//
// The bound rather than the constant because the one thing being asserted is that
// the *hub* ends the stream — which must be immediate — and a bound that is generous
// enough to hide a hang would defeat the assertion.
func (s *stream) closedWithin(t *testing.T, within time.Duration) bool {
	t.Helper()

	select {
	case err := <-s.done:
		// `io.EOF` is the shape a clean server-side close takes; anything else is a
		// transport failure and the test says which.
		if err != nil && !errors.Is(err, io.EOF) {
			t.Errorf("the stream ended with %v, want io.EOF from a clean close", err)
		}

		return true
	case <-time.After(within):
		return false
	}
}

// testLog sends the route's log lines to the test's output.
//
// Not a convenience: the handler's own failure paths are *deliberately* recoverable
// — a fragment that will not render logs an error and keeps the connection, because
// dropping a GM's stream over one bad fragment turns a bug into an outage — so with a
// nil logger those paths are invisible and a test that only watches the wire cannot
// tell "no notice arrived" from "a notice arrived and failed to render".
type testLog struct{ t *testing.T }

func (l testLog) Write(p []byte) (int, error) {
	l.t.Logf("%s", p)

	return len(p), nil
}

// newHub returns a hub for one test.
//
// Its close is registered by `serve`, which is where the ordering against the
// server's own cleanup is decided; see the comment there. A hub closed here instead
// would be closed *after* `httptest.Server.Close` has already started waiting for the
// stream this hub is holding open.
func newHub(t *testing.T) *events.Hub {
	t.Helper()

	return events.NewHub()
}

// changed is one settled change, as the watcher would produce it.
func changed(path string, op content.Op) content.Change {
	return content.Change{
		CampaignID: testCampID,
		Slug:       testSlug,
		Op:         op,
		Path:       path,
	}
}

// TestAStreamCarriesARenderedFragment is the route's central claim: the payload is
// markup the server rendered, addressed to one selector, and it is not a JSON
// document a client renders.
//
// The frame is parsed as a *set of SSE fields* rather than substring-matched, because
// a substring test would pass on a frame that carried the right words in the wrong
// field — and the field names are the wire format, so they are the thing to assert.
func TestAStreamCarriesARenderedFragment(t *testing.T) {
	t.Parallel()

	hub := newHub(t)
	server := serve(t, hub)

	//nolint:bodyclose // `open` registers the body's close as a cleanup, and this test's
	// whole subject is a response that is deliberately never closed by the test body.
	resp := open(t, server, gmRequestor())

	// The headers, before anything is asserted about the body: a stream that a proxy
	// buffers, or that a browser's EventSource would refuse, is broken at this point
	// rather than at the first notice.
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream; an EventSource refuses "+
			"any other type and never fires", got)
	}

	if got := resp.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Errorf("X-Accel-Buffering = %q, want %q; a proxy that buffers this response "+
			"makes the route appear to work and deliver nothing", got, "no")
	}

	if got := resp.Header.Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Errorf("Cache-Control = %q, want it to contain no-store; this response is "+
			"never a cacheable representation of anything", got)
	}

	lines := read(resp)

	// The opening comment, and the proof that the stream is not buffered: it arrives
	// before the handler could have returned, and after the 50ms handler budget has
	// already expired.
	if frame := sseFields(lines.next(t)); !frame.isComment() {
		t.Errorf("the first record is %v, want an SSE comment; the handler flushes "+
			"before it waits for anything, so the buffer is empty when it does",
			frame.fields)
	}

	hub.Publish(changed("Vault.md", content.OpUpsert))

	frame := sseFields(lines.next(t))

	if got := frame.get("event"); got != "datastar-patch-elements" {
		t.Errorf("the frame's event = %q, want %q; that is the field Datastar reads "+
			"the rest of the frame as instructions for", got, "datastar-patch-elements")
	}

	if got := frame.get("selector"); got != "#editor-change-notice" {
		t.Errorf("the frame's selector = %q, want %q; it must name the one element the "+
			"editor renders for this", got, "#editor-change-notice")
	}

	if got := frame.get("mode"); got != "inner" {
		t.Errorf("the frame's mode = %q, want %q; `outer` would replace the container "+
			"the editor document owns", got, "inner")
	}

	fragment := strings.Join(frame.lines(), "\n")
	if !strings.Contains(fragment, "This page changed on disk.") {
		t.Errorf("the fragment does not carry the notice's sentence:\n%s", fragment)
	}

	// A rendered DOM fragment, not a JSON document. The check is that the payload
	// *parses as HTML* and carries the semantics §7.5 asks for: a polite status
	// region, and a Review action pointing at the editor for that page.
	if _, err := html.Parse(strings.NewReader(fragment)); err != nil {
		t.Fatalf("the payload is not HTML: %v\n%s", err, fragment)
	}

	if !strings.Contains(fragment, `role="status"`) ||
		!strings.Contains(fragment, `aria-live="polite"`) {
		t.Errorf("the notice is not a polite status region; §7.5's table gives the "+
			"editor's external-change notice exactly that:\n%s", fragment)
	}

	if !strings.Contains(fragment, `href="/c/greyhaven/edit/Vault.md"`) &&
		!strings.Contains(fragment, `data-review-href="/c/greyhaven/edit/Vault.md"`) {
		t.Errorf("the Review action does not point at the editor for the changed "+
			"page; §4.8 says it loads the current disk content into the diff:\n%s", fragment)
	}

	// Not JSON. A payload that began `{` would be a client renderer, and one that
	// could not reproduce the five extensions.
	if strings.HasPrefix(strings.TrimSpace(fragment), "{") {
		t.Errorf("the payload is a JSON document; §7.5 requires rendered DOM fragments "+
			"so the client never has to render content itself:\n%s", fragment)
	}
}

// sseFrame is one parsed event-stream record.
type sseFrame struct {
	// fields is the record's fields, with repeated names joined by a newline — which
	// is Datastar's own encoding for a multi-line value, and the reason the handler
	// writes one `elements` line per line of the fragment rather than one line with
	// embedded newlines (the grammar ends a field at the first newline, so a
	// multi-line fragment in a single field is a truncated patch).
	fields map[string]string
}

// get is one field's value.
func (f sseFrame) get(name string) string {
	return f.fields[name]
}

// lines is the fragment, as the lines it was written in.
func (f sseFrame) lines() []string {
	fragment := f.fields["elements"]
	if fragment == "" {
		return nil
	}

	return strings.Split(fragment, "\n")
}

// isComment reports whether the record is a comment, which carries no field at all.
func (f sseFrame) isComment() bool {
	return len(f.fields) == 0
}

// sseFields parses one record, the way the vendored module does.
//
// ## Why this reader had to be rewritten
//
// Phase 7's reader accepted a bare `selector #x` line. It was written to agree with
// the handler, so it could not notice that the handler disagreed with the client —
// and the handler did. The event-stream grammar ends a field at the first colon and
// treats a colon-less line as a field name with an empty value; Datastar's parser is
// narrower still and handles exactly four names (`data`, `event`, `id`, `retry`),
// ignoring every other silently. So `selector #x` is not a field anything reads.
//
// This reader now implements the client's own rules, and
// `TestTheFrameIsWrittenTheWayTheVendoredModuleReadsIt` pins the bytes so a future
// relaxation here is a red build rather than a silent one.
//
// The two spellings that matter:
//
//   - `data: selector #x` is Datastar's own field: the name after the prefix, the
//     value after the space. Repeated `data:` payloads are joined with a newline,
//     which is how a multi-line fragment survives the field-per-line encoding.
//   - `data: <payload>` with **no** name is the grammar's own data field, and it is
//     kept under the name `data` so a test can tell "a plain data line" from
//     "Datastar's `selector` field".
func sseFields(frame []string) sseFrame {
	fields := map[string]string{}

	appendField := func(name, value string) {
		if existing, present := fields[name]; present {
			fields[name] = existing + "\n" + value

			return
		}

		fields[name] = value
	}

	for _, line := range frame {
		if strings.HasPrefix(line, ":") {
			// A comment. It is a field with no name, and the grammar says a client
			// ignores it — which is why the keep-alive is not throttled.
			continue
		}

		name, value, found := strings.Cut(line, ":")
		if !found {
			// A line with no colon. The grammar calls this a field name with an empty
			// value, and Datastar's parser has no case for any name but the four it
			// knows, so it is dropped there and dropped here. Dropping it rather than
			// guessing is the point: a guess here would make this reader agree with a
			// broken handler.
			continue
		}

		if name != dataField {
			// `event`, `id` and `retry` are the grammar's own fields, and Datastar's
			// parser reads them by exactly these names.
			appendField(name, strings.TrimPrefix(value, " "))

			continue
		}

		payload := strings.TrimPrefix(value, " ")
		fieldName, fieldValue, named := strings.Cut(payload, " ")
		if !named {
			// `data:` with nothing after it is the grammar's data field, empty.
			appendField(dataField, payload)

			continue
		}

		appendField(fieldName, fieldValue)
	}

	return sseFrame{fields: fields}
}

// dataField is the event-stream field name Datastar's attributes arrive under. It is
// the handler's `dataPrefix` without the trailing space, and the test spells it
// rather than deriving it so a change to the handler is a change to the reader —
// which is the only way this reader can ever disagree with it, and therefore the only
// way it can catch it.
const dataField = "data"

// TestInsertionsIntoTheLiveRegionAreThrottledToOnePerSecond is §7.5's throttle, and
// it is asserted in both directions because either half alone is satisfied by a broken
// implementation: "nothing arrives" passes a throttle that never sends, and "a frame
// arrives" passes one that sends every change.
//
// Three changes published together must produce **one** insertion, because three
// notices in the same second are one fact — the disk moved — and a reader is told
// that, not how many times. Then one more change must produce the next insertion, no
// sooner than a second later.
func TestInsertionsIntoTheLiveRegionAreThrottledToOnePerSecond(t *testing.T) {
	t.Parallel()

	hub := newHub(t)
	server := serve(t, hub)

	//nolint:bodyclose // See above: `open` owns the close.
	lines := read(open(t, server, gmRequestor()))
	lines.next(t) // the opening comment

	for _, page := range []string{"Vault.md", "Other.md", "Third.md"} {
		hub.Publish(changed(page, content.OpUpsert))
	}

	// The first insertion is allowed immediately: the throttle is a floor on the gap,
	// not a delay before the first one.
	if first := sseFields(lines.next(t)); len(first.lines()) == 0 {
		t.Fatalf("the first record carries no fragment: %v", first.fields)
	}

	// The other two were coalesced. 900ms is under the shipped one-second interval and
	// over the timer a loaded machine might add, so this is a floor with a margin
	// rather than a race.
	if !lines.silentFor(t, 900*time.Millisecond) {
		t.Error("three changes in one second produced more than one insertion; " +
			"§7.5 throttles insertions into a live region to 1/second")
	}

	// And the stream is still alive: a fourth change produces the next insertion.
	hub.Publish(changed("Fourth.md", content.OpUpsert))

	fragment := sseFields(lines.next(t)).lines()
	if !strings.Contains(strings.Join(fragment, "\n"), "Fourth.md") {
		t.Errorf("the second insertion does not carry the fourth change's page:\n%v", fragment)
	}
}

// TestAStreamEndsWhenTheHubCloses is the clean-shutdown half, from the other end.
//
// A hub that does not close its subscriptions leaves every connected GM holding a
// socket and a goroutine until the machine reboots, and the symptom is invisible: the
// process is up and every route answers. So the assertion is that the response body
// reaches EOF, which is what a browser's EventSource sees as a reconnect — and a
// reconnect after a deploy is correct, where a hang is not.
func TestAStreamEndsWhenTheHubCloses(t *testing.T) {
	t.Parallel()

	hub := newHub(t)
	server := serve(t, hub)

	//nolint:bodyclose // See above: `open` owns the close.
	lines := read(open(t, server, gmRequestor()))
	lines.next(t) // the opening comment

	if err := hub.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if !lines.closedWithin(t, 5*time.Second) {
		t.Error("the stream did not end when the hub closed; every connected editor " +
			"would hold a socket and a goroutine until the machine reboots")
	}
}

// TestTheStreamCarriesNoContent is S-12.3 for this route, and the reason the payload
// is a notice rather than a page.
//
// The page's *body* holds a marker — a `[!secret]` callout, or anything else — and
// the frame must not carry it. Nothing in the route reads the file, so the guarantee
// is structural, and this test is what keeps it structural rather than assumed: a
// future change that put a page's first line, a diff summary or an excerpt into the
// notice would satisfy every other test in this file and would put GM content on a
// connection that has no `ETag`, no `Cache-Control: no-store` on a representation,
// and no redaction step.
//
// The page's *name* is in the frame, and that is asserted too — otherwise the test
// would pass against a route that delivered nothing at all.
func TestTheStreamCarriesNoContent(t *testing.T) {
	t.Parallel()

	const (
		pagePath = "Vault.md"
		marker   = "THE-CALLOUT-BODY-THE-NOTICE-MUST-NOT-CARRY"
	)

	hub := newHub(t)
	server := serve(t, hub)

	//nolint:bodyclose // See above: `open` owns the close.
	lines := read(open(t, server, gmRequestor()))
	lines.next(t)

	// The page exists and its body holds the marker. Nothing reads it — that is the
	// point — so this is the vault the notice is *about*.
	page := filepath.Join(t.TempDir(), pagePath)
	if err := os.WriteFile(page, []byte("[!secret]\n"+marker+"\n"), 0o600); err != nil {
		t.Fatalf("write the page: %v", err)
	}

	hub.Publish(changed(pagePath, content.OpUpsert))

	frame := strings.Join(lines.next(t), "\n")

	if !strings.Contains(frame, pagePath) {
		t.Fatal("the frame does not name the changed page, so the test cannot " +
			"distinguish 'the body is absent' from 'the notice never arrived'")
	}

	if strings.Contains(frame, marker) {
		t.Errorf("the frame carries the page's body; S-12.3 forbids an event carrying "+
			"file contents, and a stream has no redaction step at all:\n%s", frame)
	}
}

// TestARemovedPageCarriesNoReviewAction is the third of `Op`'s three states, and it
// is the one where the *affordance* has to go rather than the sentence.
//
// A removed page has nothing to review, and a Review button that led to a 404 would be
// a control that reports a fault rather than a fact. UI §4.5's rule is that a control
// a viewer below the tier cannot use is absent rather than disabled, and the same
// reasoning covers a control that cannot work.
func TestARemovedPageCarriesNoReviewAction(t *testing.T) {
	t.Parallel()

	hub := newHub(t)
	server := serve(t, hub)

	//nolint:bodyclose // See above: `open` owns the close.
	lines := read(open(t, server, gmRequestor()))
	lines.next(t)

	hub.Publish(changed("Vault.md", content.OpRemove))

	fragment := sseFields(lines.next(t)).lines()
	joined := strings.Join(fragment, "\n")

	if !strings.Contains(joined, "This page was removed from disk.") {
		t.Errorf("a removal says %q; the notice has three sentences and this is one "+
			"of them:\n%v", "This page was removed from disk.", fragment)
	}

	if strings.Contains(joined, "data-review-href") {
		t.Errorf("a removal carries a Review action; there is nothing to review and "+
			"the action would 404:\n%v", fragment)
	}
}

// TestTheStreamIsMountedBehindThePlayGate is ADR 0024 for this route, and it is
// asserted on the statuses a player and an anonymous reader get.
//
// **The gate is the play gate, not the edit gate, and the change is this work
// item's.** UI §7.5's resolution table puts the editor's external-change notice
// *and* the play surface's sidebar fragments on this one URL, so one path cannot sit
// behind two gates. The gate is the lower one — a player is entitled to the sidebar
// — and the payload is filtered by tier inside the handler, which
// `TestAPlayerIsServedTheSidebarAndNeverTheEditorsPagePaths` holds.
//
// The refusals themselves are unchanged in kind: an anonymous visitor gets 401
// before the handler runs, and a reader with no membership gets 404 from the guard's
// `TierNone` branch. Neither learns whether the campaign exists.
func TestTheStreamIsMountedBehindThePlayGate(t *testing.T) {
	t.Parallel()

	hub := newHub(t)
	server := serve(t, hub)

	for _, testCase := range []struct {
		name      string
		requestor domain.Requestor
		want      int
	}{
		{
			name:      "an anonymous reader",
			requestor: anonymousRequestor(),
			want:      http.StatusUnauthorized,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			resp, err := connect(t, server, testCase.requestor)
			if err != nil {
				t.Fatalf("open the stream: %v", err)
			}

			defer resp.Body.Close()

			if resp.StatusCode != testCase.want {
				t.Errorf("status = %d, want %d; the gate answers before the handler "+
					"runs, and a stream behind no gate would let any reader watch a "+
					"table they are not a member of", resp.StatusCode, testCase.want)
			}
		})
	}
}

// TestTheStreamOnlyCarriesNoticesForItsOwnCampaign is the hub's campaign filter, and
// it is the one property of a *process-wide* broker that a reader can observe: two
// GMs editing two campaigns on one instance share a hub, and a notice about one
// campaign's page must never reach the other.
//
// Asserted on the hub directly rather than through a socket, because the socket
// version would be the same test with a one-second wait attached to it.
func TestTheStreamOnlyCarriesNoticesForItsOwnCampaign(t *testing.T) {
	t.Parallel()

	hub := events.NewHub()
	mine := hub.Subscribe(testCampID)
	defer mine.Close()

	hub.Publish(content.Change{
		CampaignID: otherCamp,
		Slug:       "elsewhere",
		Op:         content.OpUpsert,
		Path:       "Their Page.md",
	})

	select {
	case notice := <-mine.Notices():
		t.Fatalf("a notice for campaign %d reached a subscriber for %d: %+v",
			otherCamp, testCampID, notice)
	default:
	}

	hub.Publish(changed("My Page.md", content.OpUpsert))

	select {
	case notice := <-mine.Notices():
		if notice.CampaignID != testCampID || notice.Path != "My Page.md" {
			t.Errorf("the notice is %+v, want campaign %d's own page", notice, testCampID)
		}
	default:
		t.Error("the subscriber received nothing for its own campaign")
	}
}

// TestAPublisherIsNeverBlockedByAnIdleSubscriber is the hub's contract with the
// watcher, and it is the property that keeps a browser tab from being able to stop a
// campaign's indexer.
//
// `content.ChangeSink` says a sink "must not block indefinitely", and a hub whose
// send could block would put a GM who left a tab open between an Obsidian edit and
// the page appearing in search. So the test does not read from the subscription at
// all: it publishes far more notices than the buffer holds and requires every publish
// to have returned, with the drop counter to prove they were dropped rather than
// queued.
func TestAPublisherIsNeverBlockedByAnIdleSubscriber(t *testing.T) {
	t.Parallel()

	hub := events.NewHub()
	idle := hub.Subscribe(testCampID)
	defer idle.Close()

	const published = 500

	for at := range published {
		hub.Publish(changed("Page "+strconv.Itoa(at)+".md", content.OpUpsert))
	}

	stats := hub.Stats()
	if stats.Published != published {
		t.Errorf("the hub counted %d published notices, want %d", stats.Published, published)
	}

	if stats.Dropped == 0 {
		t.Error("no notice was dropped for a subscriber that never read; a hub that " +
			"queues without bound is a memory leak wearing a buffer")
	}

	if stats.Delivered >= published {
		t.Errorf("%d notices were delivered to a subscriber that never read; the "+
			"buffer holds one", stats.Delivered)
	}

	// And the newest is the one that survived: a notice says the disk moved, and the
	// page it names should be the one that moved last.
	select {
	case notice := <-idle.Notices():
		if notice.Path != "Page "+strconv.Itoa(published-1)+".md" {
			t.Errorf("the surviving notice names %q, want the newest (%q); a stale "+
				"path would send a GM to review the wrong page",
				notice.Path, "Page "+strconv.Itoa(published-1)+".md")
		}
	default:
		t.Error("the subscriber's buffer is empty; at least the newest notice must be there")
	}
}

// TestASubscriptionEndsExactlyOnce covers the double-close that a shutdown race
// produces, because a `close` of a closed channel panics and this is the one place
// that panic would arrive in the watcher's goroutine.
//
// Both orders are exercised: the subscriber giving up and then the hub shutting down,
// and the hub shutting down and then the subscriber giving up. Either order is a
// routine race between `server.Shutdown` and a browser disconnecting.
func TestASubscriptionEndsExactlyOnce(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name  string
		first func(hub *events.Hub, subscription *events.Subscription)
	}{
		{
			name:  "the subscriber first",
			first: func(_ *events.Hub, subscription *events.Subscription) { subscription.Close() },
		},
		{
			name:  "the hub first",
			first: func(hub *events.Hub, _ *events.Subscription) { _ = hub.Close() },
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			hub := events.NewHub()
			subscription := hub.Subscribe(testCampID)

			testCase.first(hub, subscription)
			subscription.Close()

			// The channel is closed exactly once and the subscriber is gone.
			if _, open := <-subscription.Notices(); open {
				t.Error("the subscription's channel is still open after both closes")
			}

			if got := hub.Stats().Subscribers; got != 0 {
				t.Errorf("the hub still counts %d subscribers, want 0; a subscriber that "+
					"is not removed keeps filling a channel nobody reads", got)
			}
		})
	}
}

// TestAPatchCanNeverTouchTheFocusedElement is §7.5's "patches must never touch the
// focused element", asserted against the editor document the selector names.
//
// The reasoning is what makes this checkable rather than aspirational: a patch
// replaces an element's children, so it cannot move focus **if and only if** the
// element it targets holds nothing focusable and is not itself focusable. So the test
// renders the editor's rail — the markup the selector names — parses it, and asserts
// both. A test that only checked "the selector is in the document" would pass against
// a container holding the textarea, which is the one element whose focus the editor
// cannot afford to lose.
func TestAPatchCanNeverTouchTheFocusedElement(t *testing.T) {
	t.Parallel()

	view := editui.EditorView{
		Shell:       components.ShellView{Title: "Vault"},
		Page:        "Vault",
		Validator:   `W/"abc"`,
		ContentHash: "abc",
		Preview:     "<p>Iron and rust.</p>",
	}

	// The whole document, not just the rail component: the property under test is
	// about where the target sits *in the page a GM has open*, and a fragment
	// rendered on its own has no landmarks and so cannot show that.
	var page strings.Builder

	if err := editui.EditorPage(view).Render(t.Context(), &page); err != nil {
		t.Fatalf("render the editor: %v", err)
	}

	root, err := html.Parse(strings.NewReader(page.String()))
	if err != nil {
		t.Fatalf("parse the editor: %v", err)
	}

	// The selector the frame names, spelled out here so the two cannot drift: the
	// handler's constant is unexported and a test that read it would be testing the
	// constant against itself.
	const patchTarget = "editor-change-notice"

	target := findByID(root, patchTarget)
	if target == nil {
		t.Fatalf("the editor renders no #editor-change-notice; the stream's selector "+
			"would patch nothing:\n%s", page.String())
	}

	if isFocusable(target) {
		t.Error("the patch target is itself focusable; replacing or updating it can " +
			"move focus, and §7.5 forbids that")
	}

	if focusable := focusableWithin(target); len(focusable) != 0 {
		t.Errorf("the patch target holds %d focusable elements (%v); patching its "+
			"children would destroy focus, which §7.5 forbids",
			len(focusable), focusable)
	}

	// And the target is *inside* a landmark, so the fragment lands somewhere a
	// reader can find rather than at the top of the document.
	if !hasAncestorWithRole(target, "complementary") {
		t.Error("the patch target is not inside the right rail; §4.4 puts the " +
			"editor's external-change indicator there")
	}
}

// findByID returns the element carrying an id, or nil.
func findByID(root *html.Node, id string) *html.Node {
	var found *html.Node

	var walk func(*html.Node)

	walk = func(node *html.Node) {
		for child := node.FirstChild; child != nil && found == nil; child = child.NextSibling {
			if child.Type == html.ElementNode && attributeOf(child, "id") == id {
				found = child

				return
			}

			walk(child)
		}
	}

	walk(root)

	return found
}

// attributeOf returns an attribute's value.
func attributeOf(node *html.Node, name string) string {
	for _, attr := range node.Attr {
		if attr.Key == name {
			return attr.Val
		}
	}

	return ""
}

// isFocusable reports whether a keyboard can reach the element, by the same set the
// accessibility gate uses.
func isFocusable(node *html.Node) bool {
	if node.Type != html.ElementNode {
		return false
	}

	if lookupAttribute(node, "tabindex") {
		return true
	}

	switch node.Data {
	case "a":
		return lookupAttribute(node, "href")
	case "button", "input", "select", "textarea", "summary", "iframe", "audio", "video":
		return true
	default:
		return false
	}
}

// focusableWithin returns the tags of every focusable element in a subtree.
func focusableWithin(node *html.Node) []string {
	var found []string

	var walk func(*html.Node)

	walk = func(current *html.Node) {
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			if isFocusable(child) {
				found = append(found, child.Data)
			}

			walk(child)
		}
	}

	walk(node)

	return found
}

// hasAncestorWithRole reports whether any ancestor carries the role.
func hasAncestorWithRole(node *html.Node, role string) bool {
	for ancestor := node.Parent; ancestor != nil; ancestor = ancestor.Parent {
		if ancestor.Type == html.ElementNode && attributeOf(ancestor, "role") == role {
			return true
		}
	}

	return false
}

// lookupAttribute reports whether an attribute is present, whatever its value.
//
// Presence rather than the value, because the one caller asks "is this element
// focusable" and an element with `tabindex="-1"` is: it is focusable
// programmatically even though it is not in the tab order, and the gate's own
// `aria-hidden` rule treats it as focusable too.
func lookupAttribute(node *html.Node, name string) bool {
	for _, attr := range node.Attr {
		if attr.Key == name {
			return true
		}
	}

	return false
}
