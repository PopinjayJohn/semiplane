// The table's two routes: the VTT document at `GET /c/{slug}/play` and the
// WebSocket at `GET /c/{slug}/ws`, plus the loop that keeps one.
//
// # Two routes, one gate, and why the split is not cosmetic
//
// S-9 fixes this pair: `/play` is the VTT — an HTML document a browser can be
// pointed at — and `/ws` is the upgrade. Until this work item the socket was
// mounted **on** `/play`, so the record's own URL scheme was unreachable: a
// player who opened `/c/{slug}/play` in a browser received a 400 from a
// WebSocket handshake rather than a table, and a client that followed the spec
// could not connect at all. Two routes on one path cannot both be true, and the
// record says which one each is.
//
// The split costs nothing that was not already paid: `Mount` still wraps both in
// `campaigns.RequirePlay`, so the gate is one line covering the pair rather than
// two lines that could drift apart — and the document is *more* exposed than the
// socket was, because a rendered page carries the reader's name, the campaign's
// live placements and the campaign navigation. A `/play` that answered without a
// gate would be the failure this package's header already calls the most
// expensive one, now on a route with an HTML body.
//
// The rest of this file is the socket. `document.go` is the other route.
//
// # What this package is and is not
//
// It is the transport, the authorisation boundary and the queue discipline. It is
// not gameplay: it holds no rule, resolves no op, and knows nothing about tokens.
// The hub owns "who is connected and what they are told" (`internal/realtime`),
// and a `Resolver` — registered in the composition root, not here — owns what an
// intent means. A reader who finds a rule in this file has found it in the wrong
// layer, and S-7.3's "roll is evaluated server-side" is enforced by `Hub.Apply`
// reading a result out of a resolver and out of nowhere else, which this package
// can only call.
//
// # Three boundaries, and they are in this order for a reason
//
//  1. **Membership.** `Mount` puts `campaigns.RequirePlay` on the route, so the
//     campaign is resolved and the tier checked before this handler is entered
//     (ADR 0024: a gate the route mounts, never a check inside a handler). That
//     placement is what makes "no amount of public visibility makes `/play`
//     public" a property of the route table rather than a sentence in a
//     comment: a public wiki page is not a public tabletop, and the only way to
//     keep that true as routes are added is for the gate to be part of mounting
//     this one. S-8's matrix is answered upstream, so every refusal this package
//     produces is on a request that was already a member — which is also why the
//     refusals here are about *this connection* and never about the campaign.
//
//  2. **`Origin`, before the handshake.** Enforced explicitly rather than left to
//     the library's default and for the reason ADR 0010 states: same-origin is
//     the rule. See `originAllowed` for the two branches, which are two different
//     facts and not one "on or off".
//
//  3. **The frames.** A socket is a public endpoint. Every byte on it is
//     untrusted input from the moment the upgrade completes, and every error
//     derived from one is untrusted too — see `classify`, which is why no
//     `err.Error()` from this route's I/O reaches a log line.
//
// # One writer, and no goroutine per connection
//
// The read loop on the request's own goroutine is the only thing that writes to
// the socket. That is not a style choice: `internal/realtime`'s header explains
// that a non-blocking send has nowhere to put a subscriber that is not reading,
// so the hub holds channels and *this* loop drains them, which is what makes
// `hub.go`'s "no goroutine per connection" true. A second writer would be a
// second writer on a `coder/websocket` connection, and that library panics on
// concurrent writes.
//
// The cost is stated rather than hidden: a loop blocked in `Read` cannot notice
// the hub closing its queues, so a shutdown is observed at the next read
// timeout. That bound is `ReadTimeout`, it is bounded, and `http.Server.Shutdown`
// does not wait for hijacked connections — so the tail is a goroutine the
// process releases on the way out, not a request that holds a graceful stop
// open. The alternative is a goroutine per connection, which `hub.go`'s own
// `TestJoiningAConnDoesNotStartAGoroutine` exists to prevent.
//
// # The request context is not the connection's lifetime
//
// `middleware.Timeout` gives every request a 25-second budget and cancels it
// when the handler has not returned, which is correct for every response it was
// written for. A table is not one of them: a client that says nothing for a
// minute is a game between turns, not a stuck handler. So the loop's I/O
// contexts are the request's context **minus its cancellation** — values kept,
// deadline dropped, for the reason `internal/httpapi/events`'s `renderNotice`
// gives — while the pre-upgrade work (the join) uses the request context
// unchanged, because that work genuinely belongs to the request.

package play

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/httpapi/middleware"
	"github.com/semiplane/semiplane/internal/realtime"
	"github.com/semiplane/semiplane/internal/web/components"
)

// The two faults `viewerFor` can find, and both are wiring faults rather than
// access decisions — which is why they are sentinels with no status of their own
// and `viewerFor`'s caller answers 500 for both.
//
// They are sentinels rather than strings because the log line needs a *class*:
// `errNoRole` and `errNoMembership` are two different bugs, and a line reading
// "play.viewer_unavailable" with no distinction between them tells an operator
// nothing they can act on.
var (
	// errNoMembership is a request the play gate admitted with no membership
	// behind it. The gate admits members, so this cannot happen through the mounted
	// route; it can happen when a handler is mounted without the gate, which is
	// exactly the mistake ADR 0024 exists to make visible.
	errNoMembership = errors.New("play: the play gate admitted a request with no membership")

	// errNoRole is a membership whose role this build has no name for. A row can
	// carry it after a downgrade that removed a role, and `Encode` would then
	// refuse to send the peer at all — so it is caught here, where the log line can
	// name the route.
	errNoRole = errors.New("play: the membership carries a role with no name in this build")
)

// nowFunc reads the clock the frame-rate window is measured on.
//
// A function rather than a `time.Now` call at each use so the rate limiter is one
// seam rather than two, and so the rule "no `time.Now` in rule code" has nothing
// to reach for here: this is transport bookkeeping, and the *only* clock it reads
// is the one bounding how fast one connection may talk.
func nowFunc() time.Time { return time.Now() }

// wrapf is the package's error spelling.
//
// It exists rather than `fmt.Errorf` at each call site because this file's errors
// are read by operators who are told the route they came from, and one prefix
// applied by one function is one prefix that cannot be forgotten.
func wrapf(format string, args ...any) error {
	return fmt.Errorf("play: "+format, args...)
}

// The bounds a connection runs under. Both are fields on `Handler` with these
// defaults, and both are fields rather than constants for one reason: they are
// the only two durations on this route, and a test that must observe either of
// them firing has to be able to make it fire in a test's lifetime rather than in
// a minute. A constant here would be a constant nothing could exercise.
const (
	// defaultReadTimeout is how long one read may block before the connection is
	// released.
	//
	// Ninety seconds, and the number is a compromise between two failures that
	// both end a table. Shorter and a table in the middle of a GM's turn — a
	// silence of a minute is ordinary — is closed and has to reconnect; longer and
	// a peer the sweep has already retired keeps its socket, its goroutine and its
	// `net/http` connection for the tail.
	//
	// What it is actually for is stated in `read`: it is how a *retirement* becomes
	// an actual close, and it is additional to staleness rather than a replacement
	// for it. `internal/realtime`'s header says the same thing from the other side
	// and the two halves are: the queue-full detector sees a peer whose application
	// is not draining, and the deadline sees a peer whose socket has stopped moving
	// while its queue happens to be empty.
	defaultReadTimeout = 90 * time.Second

	// defaultReadLimit is the transport's read bound, and it is the hub's constant
	// rather than a number restated here.
	//
	// `realtime.MaxTransportReadBytes` is documented as a transport limit that MUST
	// exceed the codec's own `MaxClientFrameBytes`, and its own test holds that
	// relationship. A number copied into this file could be lowered below the codec
	// bound without anything noticing, at which point the transport would silently
	// redefine the protocol — the stylesheet-import failure, in a constant.
	//
	// The library enforces it as a close with 1009 rather than as an error we
	// classify, which is the right layer for it: a size limit is a property of the
	// bytes and the transport already knows how to count them.
	defaultReadLimit = realtime.MaxTransportReadBytes
)

// Handler serves a campaign's table: the document at `/play` and the socket at
// `/ws`.
//
// No per-connection state: the hub is the broker, the logger is optional, and
// the two durations are the loop's bounds. Everything a connection needs beyond
// that is a local variable in `serve`, because a `Handler` is shared by every
// request in the process and a field per connection would be a data race
// wearing a struct.
//
// The document fields below are the same shape of thing: one value per process,
// set once by the composition root, read on every request. They are fields rather
// than constructor arguments because `Mount` takes a `*Handler` and the routes it
// registers need both halves.
type Handler struct {
	// Hub is the broker. Required: a route with no hub has nothing to join and
	// nothing to fan out to, and answering that with an open socket would be a
	// connection that reports itself healthy and receives nothing.
	Hub *realtime.Hub

	// Logger receives this route's lines. Nil is allowed and discards them, so a
	// test does not have to construct a logger to open a socket.
	Logger *slog.Logger

	// ReadTimeout bounds one read. Zero or negative means `defaultReadTimeout`.
	ReadTimeout time.Duration

	// ReadLimit bounds one message in bytes. Zero or negative means
	// `defaultReadLimit`.
	//
	// A field so the relationship to the codec's own bound is asserted from one
	// place rather than restated in a test: whatever an operator sets here is
	// checked against `realtime.MaxTransportFrameBytes` at request time, so a
	// configuration that would redefine the protocol is refused rather than
	// shipped.
	ReadLimit int64

	// The four document fields, and every one of them is optional. See
	// `document.go` for why each exists; the shape they share is that a zero
	// value renders a working document with one section shorter, which is the
	// same tolerance `wiki.Handler` gives its own chrome fields and the reason a
	// composition root that has not wired one yet serves a table rather than
	// panicking on the request path.
	//
	// Nil is a real state for every one of them: no instance name, no sign-out
	// form, no campaign switcher, no gameplay system and no live placements are
	// each a build or a campaign that can exist, and none of them is a fault in
	// this route.

	// Instance names the running instance and reports what is unhealthy, for the
	// banner, the footer and the document title's third part.
	Instance components.InstanceView

	// SignOutHref is where the banner's sign-out form posts. Empty renders no
	// form rather than one that posts nowhere.
	SignOutHref string

	// StatusHref is the instance status link in the footer (UI §4.2). Empty
	// omits it rather than pointing at a route that answers 404.
	StatusHref string

	// Campaigns lists the reader's own campaigns for the navigation's Campaigns
	// section. Nil omits the section entirely.
	Campaigns CampaignLister

	// Systems reports which gameplay system a campaign plays under, which is
	// where the die sheet's notation comes from. Nil, an error or a system with
	// no grammar each render §4.7's honest empty state instead of a notation
	// this build cannot justify.
	Systems Systems

	// Snapshot reads the campaign's live placements for the first server-rendered
	// document. Nil, or a campaign whose state is not open, renders the token
	// list's empty state — which is the truth until a client says otherwise, and
	// is why the field is a function rather than a `*realtime.Hub` method: the
	// hub holds no snapshot accessor by design (`h.states` is unexported and
	// `Registry.Get` answers only for states that are already live).
	Snapshot SnapshotFunc
}

// Mount registers the table's two routes on mux, both behind the play gate.
//
// S-9 fixes the pair: `/c/{slug}/play` is the VTT document and `/c/{slug}/ws` is
// the WebSocket upgrade. Mounting the socket on `/play` — which is what this
// package did until the document existed — was a spec violation that no test
// could see, because the URL a browser requests and the URL a socket dials are
// both strings and nothing holds them apart. The document now occupies `/play`,
// and `handler.serveDocument` and `ServeHTTP` each own one of the two.
//
// **The gate is mounted here and not left to the caller**, for the reason
// `events.Mount` and `edit.Mount` give: ADR 0024 says authorisation is a gate a
// route mounts and never a check inside a handler, and the strongest form of that
// is a route that mounts its own. The table is where the omission would cost most
// — a socket is a standing capability, so a table reachable without a gate is a
// campaign whose live state is readable, writable and observable by anyone who
// can reach the port — and where a caller who had to remember the gate would
// eventually register the route without it. One gate, `RequirePlay`, wraps both
// routes, so the document and the socket can never disagree about who may see the
// table.
//
// The campaign's id and the reader's role are read from the context rather than
// from the URL and the cookie, for the same reason the editor reads them there:
// the gate resolved them, and reading the slug again would be a second lookup
// that could answer a different campaign if the row had changed in between.
//
// One method, `GET`, on each route, and a `POST` to either URL is a method that
// exists nowhere in the design, so `net/http`'s 405 is the honest answer.
func Mount(mux *http.ServeMux, handler *Handler) {
	gate := campaigns.RequirePlay

	mux.Handle("GET /c/{slug}/ws", gate(handler))
	mux.Handle("GET /c/{slug}/play", gate(http.HandlerFunc(handler.serveDocument)))
}

// ServeHTTP admits a member to a campaign's table over a WebSocket.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	access := campaigns.AccessFrom(ctx)

	if h.Hub == nil {
		// A wiring fault, and a 500 rather than a silent refusal: a client that
		// connected and received an open socket would wait for a snapshot that
		// could never arrive, and a browser reports that as an intermittent
		// network error for as long as the fault lasts.
		refuse(w, http.StatusServiceUnavailable, "the table is unavailable")

		return
	}

	// `Origin` before the join and before the upgrade, and the order is the
	// argument: a cross-origin page must not be able to cause a join at all, and
	// the join is the only thing here that touches a campaign.
	if !originAllowed(r) {
		h.log(ctx, slog.LevelWarn, "play.origin_refused",
			slog.String("campaign", access.Campaign.Slug),
			slog.String("origin_host", originHost(r)),
		)

		refuse(w, http.StatusForbidden, "this page may not open a table here")

		return
	}

	viewer, err := viewerFor(ctx, access)
	if err != nil {
		// A fault in the gate rather than an access decision: `RequirePlay` has
		// already established a membership, and its absence here means the two
		// disagree. 500 because naming it as a 404 would be a claim about a
		// campaign the reader was entitled to.
		h.log(ctx, slog.LevelError, "play.viewer_unavailable",
			slog.String("campaign", access.Campaign.Slug),
			slog.String("class", realtime.FrameClass(err)),
		)

		refuse(w, http.StatusInternalServerError, "this page could not be loaded")

		return
	}

	// The join is before the upgrade, and that is the whole argument for it: every
	// way the join can fail — a closed hub, a campaign at `MaxPeersPerCampaign`, a
	// state that will not open — is a *status code*, and a status code is only
	// available before the response is committed to being a socket. Upgrading first
	// would turn all of them into a 101 followed by a close frame, which every
	// browser reports as the same anonymous failure.
	//
	// The cost is a peer slot occupied by a handshake that then fails, and it is
	// released by the deferred `Leave` below on every path out of `serve`.
	peer, err := h.Hub.Join(ctx, access.Campaign.ID, viewer)
	if err != nil {
		status, message := joinRefusal(err)

		h.log(ctx, slog.LevelWarn, "play.join_refused",
			slog.String("campaign", access.Campaign.Slug),
			slog.Int("status", status),
			slog.String("class", realtime.FrameClass(err)),
		)

		refuse(w, status, message)

		return
	}

	defer peer.Leave()

	h.serve(ctx, w, r, access.Campaign.Slug, peer)
}

// originAllowed reports whether r's `Origin` permits an upgrade, and the two
// branches are two different facts rather than one switch.
//
// `Origin` is a **browser** header. It exists for exactly one purpose — telling a
// server that a page on some other site is the thing asking — and a browser sends
// it on every WebSocket handshake, always, from `http://` and `https://` alike. So
// the two cases are not "on or off":
//
//   - **Absent.** There is no cross-origin vector to refuse, because the attack
//     requires a browser and a browser does not produce this request. The clients
//     that legitimately send no `Origin` are non-browser ones: a test harness, a
//     server-side integration, an operator at a `wscat` prompt, the demo check.
//     Refusing them would buy nothing and cost a self-hosted instance its own
//     tooling. **Refused would be the wrong answer, and so would accepted: the
//     absence is a fact this handler decides on, not a fact it failed to check.**
//
//   - **Present.** It must be an `http` or `https` origin on this host. Anything
//     else is a page on another site asking for a socket it is not entitled to,
//     and the session cookie rides along automatically, which is the whole attack.
//     `Origin: null` — what a sandboxed iframe and a `file://` page send — is
//     refused explicitly rather than by failing to parse, because "it did not
//     match" is a weaker reason than "it named no host".
//
// The comparison is on the **host**, case-insensitively, and the scheme is
// checked for being a scheme this application is served over rather than
// compared: a `ws://` scheme in an `Origin` is not something a browser emits, and
// a deployment terminating TLS in front of this process must still accept the
// `https` origin its own certificate implies.
func originAllowed(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		// Branch one: no browser, therefore no cross-origin request, therefore
		// nothing to refuse. Stated as a decision so that a reader auditing this
		// function sees an answer rather than an absence of one.
		return true
	}

	// Branch two: a browser, so the origin must be this instance.
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}

	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return false
	}

	return strings.EqualFold(parsed.Host, r.Host)
}

// originHost reports the `Origin` header's host, for a log line.
//
// A reduced value rather than the header: the header is client-chosen and the
// full string carries a scheme and a port that are both attacker-chosen, and the
// question an operator is asking of this line is "which site tried", which is the
// host. Empty for an absent or unparseable origin, which is itself the answer to
// "was this a browser".
func originHost(r *http.Request) string {
	parsed, err := url.Parse(strings.TrimSpace(r.Header.Get("Origin")))
	if err != nil {
		return ""
	}

	return parsed.Host
}

// viewerFor binds the identity and role the gate resolved into the hub's `Viewer`.
//
// From the context and not from the request: `campaigns.Requestor` reads the
// identity off the context so a handler cannot pass one request's identity into
// another's access decision, and the role comes from the membership the gate
// already looked up. **Nothing here re-checks access.** ADR 0024 makes a check
// inside a handler the shape the record was written about, and S-8's "no access is
// 404" cannot be reproduced from inside a hub at all — the gate is the only place
// in this project that can answer it, and by the time this function runs it has.
//
// The nil membership is a 500 rather than a refusal, and the distinction matters:
// `RequirePlay` admits a member, so a nil membership here means the gate and this
// function disagree about the same request, which is a bug and not an access
// decision. Saying 404 would attribute a fault to the reader's permissions.
func viewerFor(ctx context.Context, access campaigns.Access) (realtime.Viewer, error) {
	if access.Membership == nil {
		return realtime.Viewer{}, errNoMembership
	}

	// A role this build has no name for is refused here rather than at the hub, so
	// the log line names the *route* as the thing that could not bind an identity.
	// `Join` refuses it too, and that is the second of two gates rather than a
	// backup for a missing one: the point of the check is that a membership row
	// naming an unknown role produces a 500 with this route's name on it.
	if !access.Membership.Role.Valid() {
		return realtime.Viewer{}, errNoRole
	}

	return realtime.Viewer{
		ID:   realtime.UserID(campaigns.Requestor(ctx).UserID),
		Role: access.Membership.Role,
	}, nil
}

// joinRefusal maps a `Hub.Join` failure to the status and body this route answers
// with.
//
// Every entry is a *this connection* failure and none is a statement about the
// campaign's access, because the gate has already answered that and S-8 requires
// the two to be indistinguishable to anyone who is not entitled.
//
// The two capacity answers differ on purpose. A closed hub is 503 with a retry
// hint, because it is this instance shutting down and the same URL will work in a
// moment. A full campaign is 503 as well, and a *different* 503, because both are
// "not now" and a client that retries is the correct behaviour for each. A state
// that will not open is 500: that is a fault an operator must see, and a GM whose
// table refused to open for a reason they cannot act on is worse than one told to
// retry.
func joinRefusal(err error) (int, string) {
	switch {
	case errors.Is(err, realtime.ErrHubClosed):
		return http.StatusServiceUnavailable, "the table is shutting down"
	case errors.Is(err, realtime.ErrCampaignFull):
		return http.StatusServiceUnavailable, "this table is full"
	default:
		return http.StatusInternalServerError, "this page could not be loaded"
	}
}

// refuse answers a request that must not become a socket.
//
// `private, no-store` and not merely `no-store`, for the reason AGENTS.md states
// for every gate response: a reverse proxy in front of a self-hosted instance is
// the ordinary deployment, and a cached refusal is a refusal served to somebody
// who was entitled. This route's refusals are 403s about *this connection* rather
// than about the campaign, but the same proxy would happily cache them, and a
// cached 403 on `/play` is a table that stays shut until an operator clears a
// cache.
//
// A short plain-text body, and deliberately **not** a rendered document. The
// client for this URL is a WebSocket, which never renders a refusal body, and the
// one reader who can — somebody who typed the path into a browser — gets a
// sentence. A shell document here would be a second rendered document in the
// project with no §10.2 audit over it, and the a11y gate's rule is that naming a
// route is a claim of audits. A sentence cannot break a landmark audit because it
// has no landmarks.
func refuse(w http.ResponseWriter, status int, message string) {
	header := w.Header()
	header.Set("Content-Type", "text/plain; charset=utf-8")
	header.Set("Cache-Control", "private, no-store")

	w.WriteHeader(status)
	//nolint:errcheck // The status line is already committed; a failed body write
	// leaves nothing to answer with, and the same failure is reported by the
	// access log's status.
	_, _ = w.Write([]byte(message + "\n"))
}

// log writes one line, discarding it when no logger is configured.
func (h *Handler) log(ctx context.Context, level slog.Level, event string, attrs ...slog.Attr) {
	if h.Logger == nil {
		return
	}

	h.Logger.LogAttrs(ctx, level, "play",
		append([]slog.Attr{
			slog.String("event", event),
			slog.String("request_id", middleware.MustRequestID(ctx)),
		}, attrs...)...)
}

// readTimeout returns the read bound, defaulted.
func (h *Handler) readTimeout() time.Duration {
	if h.ReadTimeout > 0 {
		return h.ReadTimeout
	}

	return defaultReadTimeout
}

// readLimit returns the message bound, defaulted **and refused if it is too small**.
//
// The floor is the codec's own bound, and it is checked per request rather than
// only defaulted: `ReadLimit` is an operator-visible field, and a value below
// `realtime.MaxClientFrameBytes` would let the transport decide that a frame is
// too big before the codec ever sees it — the same silent redefinition of the
// protocol that a copy of the constant would cause, reachable by configuration
// instead of by editing. Refusing it here makes the relationship hold for every
// input rather than for the one the default happens to be.
func (h *Handler) readLimit() int64 {
	if h.ReadLimit <= 0 {
		return defaultReadLimit
	}

	return max(h.ReadLimit, realtime.MaxClientFrameBytes)
}
