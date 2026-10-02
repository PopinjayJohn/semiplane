package main

// The two invariants that belong to the composition root rather than to any of
// the five route packages it assembles.
//
// # Why these are here and not in the routes' own tests
//
// Each of the five route packages tests itself thoroughly, and each of them
// passes. What none of them can see is the property that only exists *between*
// them:
//
//   - `wiki` and `edit` each declare their own named type over
//     `map[string]*content.Renderer` and each is handed a map by this package. A
//     test inside either package builds its own map, so a composition root that
//     built two would pass every test in both packages while the wiki route
//     rendered a page the editor could not preview.
//   - `events` is the only long-lived response the process serves, and the only
//     route whose handler does not return. Nothing inside `events` knows that
//     `http.Server.Shutdown` waits for in-flight requests, so nothing inside it
//     can notice that a hub closed *after* the drain turns every shutdown into a
//     timeout.
//
// Both are assertions about this package, which is why they are here. AGENTS.md
// calls a wiring test the only kind that sees this class of fault, and the
// missing-`Authenticate` bug in `wiring_test.go` is the precedent.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/auth"
	"github.com/semiplane/semiplane/internal/httpapi/edit"
	"github.com/semiplane/semiplane/internal/httpapi/events"
	"github.com/semiplane/semiplane/internal/httpapi/wiki"
	"github.com/semiplane/semiplane/internal/store"
)

// TestTheEditorAndTheWikiRouteHoldTheSameRenderers is the invariant behind
// `editorRenderers`.
//
// `wiki.CampaignRenderers` and `edit.CampaignRenderers` are two *named* types
// declared by two sibling packages over the same underlying type, which is
// deliberate — a route does not import a sibling route — and it is also the
// easiest thing in the composition root to get wrong. Ranging over the map into a
// second one compiles, reads identically, and produces an instance where a
// campaign renders on the wiki route and answers a load error in the editor, with
// nothing in the logs to say which side is wrong.
//
// The conversion is what makes it impossible: Go does not copy a map when
// converting between two named types with the same underlying type, so
// `edit.CampaignRenderers(wikiRenderers)` shares one header. This asserts the
// sharing rather than the shape, by **mutating through one and reading through
// the other** — a test that only compared the two maps for equality would pass on
// two separate maps holding equal entries, which is exactly the bug.
func TestTheEditorAndTheWikiRouteHoldTheSameRenderers(t *testing.T) {
	t.Parallel()

	wikiRenderers := wiki.CampaignRenderers{
		"greyhaven": content.NewRenderer("greyhaven", pageKinds{}),
	}
	editRenderers := editorRenderers(wikiRenderers)

	// A campaign the composition root adds *after* both routes were built, which
	// is the shape of the bug: the wiki route would find it and the editor would
	// not.
	const latecomer = "blackgate"

	wikiRenderers[latecomer] = content.NewRenderer(latecomer, pageKinds{})

	if _, err := editRenderers.Renderer(latecomer); err != nil {
		t.Errorf("a campaign added to the wiki route's renderer map is not visible to "+
			"the editor: %v. `editorRenderers` must convert the map, not copy it — "+
			"two maps are two answers to \"which campaigns have a renderer\"", err)
	}

	// And the other direction, because a *copy* passes the check above for a
	// campaign added before the conversion and fails it for one added after, and a
	// test that walks one direction only does not know which it has.
	if _, err := wikiRenderers.Renderer(latecomer); err != nil {
		t.Errorf("a campaign added through the editor's renderer map is not visible to "+
			"the wiki route: %v", err)
	}
}

// TestTheRevisionSeamReachesTheWriterQueue is the observable end of the
// `Revisions` closure in `newEditRoute`.
//
// `store.Store.Write` takes an **unexported** parameter type, `store.writeFunc`,
// and Go requires an interface method's parameter types to be identical rather
// than merely assignable. No interface outside `store` can name it, which is why
// the composition root hands the editor a closure instead of a handle — and why
// that closure is the only sanctioned write path in the project. `Store.DB()` is
// documented for reads, and a request goroutine writing on it bypasses the queue
// whose entire job is to keep exactly one statement in flight so SQLite's
// single-writer limit is never contended. Two writers is a `SQLITE_BUSY`, and the
// one that loses is a GM's save.
//
// "Bypasses the queue" is not observable from outside the store, so what is
// asserted here is the thing that *is*: a save through the editor, driven by the
// real router the binary builds, lands a row in the real table. If the closure
// were wired to anything else — a nil seam, a second `sql.DB`, a no-op — this is
// where it would show.
//
// Not parallel, for the reason `store.Open` documents: it claims the process's one
// store slot, so a parallel test would fail on whichever fixture lost the race.
func TestTheRevisionSeamReachesTheWriterQueue(t *testing.T) {
	inst := newInstance(t)

	const page = "Goblin.md"

	inst.writePage(inst.campaign, page,
		"---\ntitle: Goblin\n---\n\nIt watches the gate.\n")

	handler := inst.serve(inst.registered)
	cookie := inst.session()

	// Read the editor to learn the page's current validator, which is the whole
	// point of the two-step protocol: a `PUT` carries the validator the client
	// holds, and the route compares it against the disk before writing anything.
	opened := httptest.NewRecorder()
	handler.ServeHTTP(opened, inst.editorRequest(
		http.MethodGet, "/c/"+fixtureSlug+"/edit/Goblin", cookie, "", nil,
	))

	if opened.Code != http.StatusOK {
		t.Fatalf("GET the editor = %d, want 200: %s", opened.Code, excerpt(opened.Body.String()))
	}

	validator := opened.Header().Get("ETag")
	if validator == "" {
		t.Fatal("the editor served no ETag, so a save has nothing to send as If-Match")
	}

	saved := httptest.NewRecorder()
	handler.ServeHTTP(saved, inst.editorRequest(
		http.MethodPut,
		"/c/"+fixtureSlug+"/edit/Goblin",
		cookie,
		validator,
		strings.NewReader(
			"---\ntitle: Goblin\n---\n\nIt watches the gate, and it is owed a week.\n",
		),
	))

	if saved.Code != http.StatusNoContent {
		t.Fatalf("PUT the editor = %d, want 204: %s", saved.Code, excerpt(saved.Body.String()))
	}

	// The row, counted against the migrated table rather than against a spy. S-14's
	// row for this route is "a successful save writes a row", and the only way to
	// assert it is to ask the table.
	rows, err := inst.countRevisions(inst.campaign.ID, page)
	if err != nil {
		t.Fatalf("count the revision rows: %v", err)
	}

	if rows != 1 {
		t.Errorf("%d revision rows for %s, want 1. The `Revisions` closure in "+
			"`newEditRoute` must reach `store.Store.Write`; a seam that did not "+
			"would leave every save unrecorded, and the vault is the only thing that "+
			"cannot be rebuilt from the database (ADR 0008)", rows, page)
	}

	// The refusal half, because a 412 that appended would make the count above
	// meaningless. One stale save must add nothing.
	stale := httptest.NewRecorder()
	handler.ServeHTTP(stale, inst.editorRequest(
		http.MethodPut, "/c/"+fixtureSlug+"/edit/Goblin", cookie, validator,
		strings.NewReader("---\ntitle: Goblin\n---\n\nA different edit entirely.\n"),
	))

	if stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("the stale PUT = %d, want 412: %s", stale.Code, excerpt(stale.Body.String()))
	}

	rows, err = inst.countRevisions(inst.campaign.ID, page)
	if err != nil {
		t.Fatalf("re-count the revision rows: %v", err)
	}

	if rows != 1 {
		t.Errorf("%d revision rows after a refused save, want 1. S-6.3 is structural: "+
			"there is no path from a failed comparison to an append", rows)
	}
}

// excerpt truncates a body for a failure message, because a whole rendered
// document in a test failure is unreadable and the first line of the reason is
// usually in the first 200 bytes.
func excerpt(body string) string {
	const limit = 200
	if len(body) <= limit {
		return body
	}

	return body[:limit] + "..."
}

// session returns a cookie for a signed-in GM of the fixture's campaign.
//
// A session row written **directly** rather than by posting to `/login`, and the
// reason is that `instance.serve` builds the router with no account routes at all
// — the fixture exists to exercise the campaign surface, and a sign-in round trip
// would be a dependency on a subsystem these tests are not about. What matters is
// that the session is real: `identity.Authenticate` resolves the cookie to a
// `users` row, `campaigns.Resolve` reads that identity to load a membership, and
// `RequireEdit` reads the role. A fabricated requestor would skip the two of those
// that decide whether the editor is reachable at all, which is the property the
// test is built on.
func (i *instance) session() *http.Cookie {
	i.t.Helper()

	token, hash, err := auth.NewSessionToken()
	if err != nil {
		i.t.Fatalf("mint a session token: %v", err)
	}

	if _, err := i.store.CreateSession(i.t.Context(), store.AuthSession{
		TokenHash: hash,
		UserID:    i.owner.ID,
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		i.t.Fatalf("create the session: %v", err)
	}

	return &http.Cookie{Name: auth.SessionCookieName, Value: token, Path: "/"}
}

// editorRequest builds one request against the editor route.
//
// The `Content-Type` and the `If-Match` are set on every method including the
// `GET`, which is harmless — a `GET` reads neither — and keeps the one call site
// from having to know which of the two it is. The body is nil for the `GET`,
// because `httptest` needs a body type it can read and `http.NoBody` says "there
// is none" in a way a handler can tell.
func (i *instance) editorRequest(
	method, path string,
	cookie *http.Cookie,
	ifMatch string,
	body io.Reader,
) *http.Request {
	i.t.Helper()

	if body == nil {
		body = http.NoBody
	}

	request := httptest.NewRequestWithContext(i.t.Context(), method, path, body)
	request.Header.Set("Content-Type", "text/markdown; charset=utf-8")

	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}

	if cookie != nil {
		request.AddCookie(cookie)
	}

	return request
}

// countRevisions is how many `page_revisions` rows exist for one page.
//
// A raw query through `DB()` rather than through a store method, and that is the
// point: `internal/store` has no read for `page_revisions` in this phase, and
// adding one is another work item's file. The *read* is what the handle is
// documented for, so using it here is also a demonstration that the read and write
// paths are genuinely different.
func (i *instance) countRevisions(campaignID int64, page string) (int, error) {
	row := i.store.DB().QueryRowContext(i.t.Context(),
		`SELECT COUNT(*) FROM page_revisions WHERE campaign_id = ? AND path = ?`,
		campaignID, page,
	)

	var count int
	// Wrapped rather than returned bare: a bare `*sql.Row.Scan` error says "scan
	// failed" and nothing about which page's rows were being counted.
	if err := row.Scan(&count); err != nil {
		return 0, fmt.Errorf("scan the revision count for %s: %w", page, err)
	}

	return count, nil
}

// TestTheEventHubClosesBeforeTheServerDrains is the shutdown-order invariant, and
// it is asserted through a real socket rather than through a mock.
//
// The claim: closing the hub ends every open event stream, which is what lets
// `http.Server.Shutdown`'s drain finish. `events.stream`'s loop ends on the hub's
// close or on a failed write and deliberately *not* on the request context's
// cancellation — selecting on that would cut every stream at the handler budget
// and leave the client reconnecting forever. So a hub closed after the drain is a
// drain that waits for a request which will not return, until the shutdown budget
// expires, on every shutdown, with `context deadline exceeded` in the log.
//
// The shape here is the real one: a server, a live stream, and a drain. A test that
// called `serve` with a stand-in handler and asserted that a callback ran would
// prove only that the callback is wired, which is the nil-tolerance property
// wearing a shutdown test's clothes.
func TestTheEventHubClosesBeforeTheServerDrains(t *testing.T) {
	t.Parallel()

	hub := events.NewHub()

	// A handler that flushes its headers and then reads until the hub closes it,
	// which is what `events.stream` does and exactly as long-lived as the test
	// needs. The flush matters: without it the client below would block waiting for
	// response headers, and the test would be measuring the wrong thing.
	//
	// `http.Server` and a real listener rather than `httptest.NewServer`, because
	// the operation under test *is* `http.Server.Shutdown` and its wait-for-idle
	// semantics are the whole claim. `httptest.Server.Close` does a related but
	// different thing — it also waits on client-side connection state — so a test
	// using it would be asserting about a proxy for the property.
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	streamed := make(chan struct{})

	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			subscription := hub.Subscribe(1)
			defer subscription.Close()

			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.WriteHeader(http.StatusOK)

			if flusher, canFlush := w.(http.Flusher); canFlush {
				flusher.Flush()
			}

			for range subscription.Notices() {
			}

			close(streamed)
		}),
		// No write deadline and no handler timeout: this server is the *worst* case
		// the drain has to survive, which is the point. A stream that a timeout
		// would have ended is a stream whose shutdown order does not matter.
		ReadHeaderTimeout: time.Second,
	}

	serving := make(chan struct{})

	go func() {
		defer close(serving)

		if serveErr := server.Serve(
			listener,
		); serveErr != nil &&
			!errors.Is(serveErr, http.ErrServerClosed) {
			t.Errorf("serve: %v", serveErr)
		}
	}()

	t.Cleanup(func() {
		_ = server.Close()
		<-serving
	})

	// The client opens the stream and *keeps the body open*, which is what keeps
	// the request in flight and is what the drain has to wait for.
	address := "http://" + listener.Addr().String()

	// `NewRequestWithContext` and an explicit `Do` rather than `http.Get`, because
	// `Get` cannot be cancelled and this request is deliberately long-lived: the
	// test needs it to stay open across the drain, and a request that cannot be
	// cancelled is a request that outlives a failing test.
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, address, http.NoBody)
	if err != nil {
		t.Fatalf("build the stream request: %v", err)
	}

	// is what keeps the request in flight for the drain to wait on.
	stream, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("open the stream: %v", err)
	}

	t.Cleanup(func() { _ = stream.Body.Close() })

	// The hub close is what `serve` does between the listener stopping and the
	// drain beginning. Deliberately *before* `Shutdown`, and that ordering is the
	// assertion.
	hub.Close()

	// The drain, with a budget long enough that a stream which correctly ended has
	// plenty of room and one that did not is unmistakably stuck, and short enough
	// that the test fails rather than hangs.
	const budget = 10 * time.Second

	shutdownCtx, cancel := context.WithTimeout(t.Context(), budget)
	defer cancel()

	drained := make(chan error, 1)

	go func() { drained <- server.Shutdown(shutdownCtx) }()

	select {
	case err := <-drained:
		if err != nil {
			t.Errorf("Shutdown = %v, want nil; a stream that ended on the hub's "+
				"close leaves the drain nothing to wait for", err)
		}
	case <-time.After(budget + time.Second):
		t.Fatalf("the server did not drain within %s while an event stream was "+
			"open. `serve` closes the hub before calling Shutdown for this reason: "+
			"Shutdown waits for in-flight requests, and an event stream's loop "+
			"deliberately ignores the request context, so it ends only on the hub's "+
			"close or a failed write", budget)
	}

	select {
	case <-streamed:
	case <-time.After(time.Second):
		t.Error("the stream handler did not return after the hub was closed")
	}
}

// TestTheSettledFanOutRunsBothSinks is the other fan-out invariant: the notice sink
// is *added to* the index sink, not substituted for it.
//
// The failure this exists to catch is a one-line change that reads like a
// simplification — `settledFanOut{index: ..., notices: ...}` becoming
// `notices: ...` alone — and it produces an instance where the `pages` table
// silently stops being written on the event path. Every surface reports success:
// the wiki route renders (from the cache, or from the startup index), search
// returns rows (written at boot), and the editor previews (reading the vault
// directly). Only the next boot's startup index reveals that nothing has been
// keeping the table equal to the tree since the change was made.
//
// Both are recorded, in order, and the order is asserted because it is a
// dependency: a notice tells a GM to reload, and a reload issued before the row
// lands answers 404 for a page the GM is looking at.
func TestTheSettledFanOutRunsBothSinks(t *testing.T) {
	t.Parallel()

	var order []string

	change := content.Change{CampaignID: 7, Slug: "greyhaven", Path: "Goblin.md"}

	fan := settledFanOut{
		index: func(context.Context, content.Change) { order = append(order, "index") },
		notices: func(context.Context, content.Change) {
			order = append(order, "notices")
		},
	}

	fan.settle(t.Context(), change)

	if len(order) != 2 {
		t.Fatalf("the fan-out ran %v, want both sinks; a fan-out that replaced the "+
			"indexer would leave the pages table unwritten on the event path and "+
			"every surface reporting success", order)
	}

	if order[0] != "index" || order[1] != "notices" {
		t.Errorf("the fan-out ran %v, want [index notices]: the indexer must go "+
			"first, because a notice tells a reader to reload and a reload issued "+
			"before the row lands answers 404 for the page they are looking at", order)
	}

	// A nil notice sink is a process with no event hub, and the indexer still has
	// to run. Nil-tolerance is the difference between "no editor" and "no index".
	ran := 0

	settledFanOut{
		index: func(context.Context, content.Change) { ran++ },
	}.settle(t.Context(), change)

	if ran != 1 {
		t.Errorf("with no notice sink the indexer ran %d times, want 1", ran)
	}
}

// The route interfaces the composition root hands its handles to, stated once so
// that a change to any of them is a compile error naming this file.
//
// `search.Pages` and `store.PageSearch` are named through their own packages'
// interfaces rather than restated, because restating a method signature in a
// `var _` is a second copy of it — and a second copy is what drifts.
var (
	_ interface {
		SearchPages(
			ctx context.Context,
			search store.PageSearch,
			req domain.Requestor,
		) ([]domain.SearchHit, error)
	} = (*store.Store)(nil)

	// `edit.Revisions` is deliberately **not** in this list. It cannot be: its
	// implementation closes over `store.Store.Write`, whose parameter type is
	// unexported, so the only expression of the seam is a closure. See
	// `newEditRoute` and `TestTheRevisionSeamReachesTheWriterQueue`.
	_ edit.Revisions = edit.NewRevisionLog(nil)

	// The fan-out's index half is the indexer's own sink, named so that a signature
	// change to `content.ChangeSink` is caught here rather than at the one call
	// site that matters.
	_ content.ChangeSink = (&content.Indexer{}).HandleChange
)

// `sql` is imported for the `database/sql` types the editor's writer closure
// names. The closure in `newEditRoute` has a parameter of type
// `func(context.Context, *sql.Tx) error`, and that signature is what the store's
// unexported `writeFunc` is compatible with — so the import is load-bearing for
// the one conversion in this package that no interface can express.
var _ = sql.LevelDefault
