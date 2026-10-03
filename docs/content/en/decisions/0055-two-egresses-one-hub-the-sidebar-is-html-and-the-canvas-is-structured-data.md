---
title: "0055 — Two egresses, one hub: the sidebar is HTML and the canvas is structured data"
description: "UI §7.5's resolution table is implemented as one `events.Hub` with two egress representations. The canvas takes structured deltas over `/c/{slug}/ws`; the sidebar takes rendered DOM fragments over `/c/{slug}/events`. The delivery policy splits on one question — does the newer value make the older one untrue, or merely old? — and the region a fragment announces is a field on its type, refused server-side when it disagrees with its target. The stream's gate is the play gate rather than the edit gate, because one URL carries two payloads. Three of §7.5's four prohibitions are code rather than comment, and the fourth — search has no live region — was already held elsewhere. Separately: the vendored Datastar is 1.0.4 from the upstream release rather than the newest *published* npm package, because every pre-1.0.0 build renamed the event this route writes."
lede: "The canvas needs data and the sidebar needs HTML, so there are two connections. Everything expensive here follows from that: two delivery policies on one hub, one gate for two payloads, and a region on the fragment's type rather than in the markup — because a client is not the party that knows which region a thing belongs in."
weight: 4
date: "2026-10-03"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

The plan contradicts itself about the play page and UI §7.5 resolves it. Architecture `§7` says "no SSE on the VTT page"; UI `§3.1` lists initiative, chat and presence as Datastar/SSE fragments; `§9` routes `/c/{slug}/events` as "SSE (editors + live chrome)"; and `§7.1`'s WebSocket protocol also carries presence. §7.5 resolves it in one table:

| Channel | Carries |
|---|---|
| **WebSocket** `/c/{slug}/ws` | client to server: intents, presence. server to client: ordered state — placement deltas, rolls, clock, snapshot, `applied`/`rejected` — consumed by PixiJS as structured data |
| **SSE** `/c/{slug}/events` | server to client: **rendered DOM fragments** for the sidebar — initiative, chat, dice log, connection and degraded notices — patched by Datastar. Plus the editor's external-change notice on `/edit` |

Two connections on the play page, against plan `§7`'s ceiling of about six. Presence rides the WebSocket because `§7.1`'s protocol already defines it there and it needs no versioning. `§7`'s "no SSE on the VTT page" is read as **no SSE for map state**.

Phase 7 built the SSE half for one payload: the editor's external-change notice, behind `campaigns.RequireEdit`, with the handler carrying no gameplay and no state. The sidebar adds four fragment families, a second reader tier, and three rules that have to be code rather than prose:

- patches must never touch the focused element;
- insertions into a live region are throttled to 1/second, and a keep-alive comment is not an insertion;
- the chat and dice logs must not replay history into a live region on connect.

And one that is already held elsewhere: **search has no live region, because search is not live.**

There is also a defect the last of these makes expensive to leave alone. §7.5's resolution table is a statement about a wire protocol, and phase 7's frames do not match the client they claim to speak to. See the "Measured" section below, because the measurement is the reason this record exists as well as the design.

## Decision

**One hub, two egress representations, and the representation is a field on the change.** `events.Notice` gains a `Kind`, whose zero value is phase 7's filesystem change — so every existing publisher and every existing test is unchanged by the field's arrival, and a new publisher that forgets the field gets phase 7's behaviour rather than a zero value nobody handled. `PublishSidebar` is a **separate method** rather than a flag, and it refuses `KindChange`: a caller reaching for the sidebar's entry point with a filesystem change has made a mistake a counter should record, and `KindChange` is the one value for which both delivery policies are wrong.

**The delivery policy splits on one question: does the newer value make the older one *untrue*, or merely *old*?** A fact about the present — the order, whose turn it is, whether the watcher is degraded — is superseded by a newer statement of the same fact, so one buffer slot is correct and dropping the older one is free. An event that happened — a chat line, a roll — is not superseded by the next one, so coalescing two leaves a transcript with a hole in it. That gives `Subscription` two channels: `Notices` with one slot and coalescing-on-full, `Lines` with sixty-four slots and oldest-out-on-full. Phase 7's one-slot buffer would have dropped four of five chat lines in a burst, and no test of the filesystem notice would have noticed.

**One ticker, one budget, and it gates every insertion.** §7.5's rule and §7.10's prohibition are one statement about the *region*, not about the event that happened to fill it, so a chat line and a page edit compete for the same budget rather than each getting their own. The coalescing slot drains first, in §7.5's own row order: a turn change and a chat line in the same second produce the turn change now and the line a second later, because the turn is the fact the reader was waiting on and the line is a thing they will still get.

**A keep-alive is a comment and the throttle does not apply to it.** It writes no fields and inserts nothing; it exists so an intermediary's idle timer and a vanished socket are both noticed. The handler's keep-alive branch touches neither slot, which is the claim `TestAKeepAliveIsNotAnInsertionAndDoesNotConsumeTheBudget` measures with a five-millisecond keep-alive — a hundredfold more often than the shipped fifteen seconds — and asserts that announcements are still a second apart.

**The region is a field on the fragment's type, and the server refuses a mismatch.** `live.Target` declares the region its element *is*; `live.Fragment` declares the region the fragment *announces*; `live.Decide` refuses the two disagreeing. §7.5's announced-content table is the specification, so encoding it in two types rather than in markup means a component change cannot move a dice roll into an assertive region without the build failing. `Decide` also refuses a fragment that parses to **any** focus stop, and it parses on every patch rather than trusting a per-family verdict: a patch is at most one a second and a fragment is a few hundred bytes, and the alternative is a rule that a later edit to one templ component breaks with nothing failing.

**The tracker is silent, and its current turn is `aria-current` rather than a sentence.** §7.5's table has a row for "turn change" and none for "the order changed". A tracker that announced itself would read the whole order aloud on every re-order, which is the replay prohibition wearing different clothes. `aria-current="true"` on one row is state a reader can ask about, which is UI §7.6's argument about the canvas and the token list applied to the tracker.

**The logs are empty at load and the histories are `aria-live="off"` beside them.** The split `components/chat` already makes for its own log, applied to the dice log here, and the assertive region follows it too: the `role="alert"` element is the **patch target** and is empty, and the conditions the document already knew are in a sibling marked `off`. A reader who arrives at a page whose watcher is already degraded has not *transitioned* — and a live region that fires on load is §7.5's search-route mistake one level down. The visually-hidden headings are **siblings** of both regions rather than children, because what a live region contains at the moment it appears is the one thing the replay rule is about, and whether a given browser announces it is not a question this product should be relying on an answer to.

**The stream's gate is the play gate, and the payload is filtered by tier.** One URL cannot sit behind two gates, and a player needs the sidebar and must not have the editor's notice — so the gate is the lower one and the handler emits the notice only for a tier that can edit. This is not [0024]({{ "decisions/0024-authorisation-gates-mount-not-per-handler/" | relURL }}) being bent: the gate decides whether the reader may see the campaign's **table** at all, before the handler runs, and filtering which fragments an entitled reader is told about is the job `internal/httpapi/wiki` does with secrets. A page path is the campaign's own directory structure, so a player receives nothing at all when the watcher publishes a change — not a redacted notice, not an empty one.

**The connection state ships as prose and the client reveals it.** `live.SocketState` renders both sentences into one assertive container with `hidden` on the one that is not currently true; the client toggles an attribute and authors no English. §7.5 puts "connection lost" on the WebSocket because the connection is what dropped, and a patch target for it would mean the notice arrives over the channel whose loss is the condition — the one design that guarantees the reader who needed it most is the one who does not get it. So `SocketHook` exists and is deliberately **not** one of the five targets, and a test says so.

**Both fragments and their mount points are server-rendered, and each fragment's target is data on the fragment.** The five targets are `[data-chrome="…"]` attribute selectors declared by `internal/web/components/live`, read from Go constants so there is one spelling. The chat log's is `chat.LogChromeHook` and **this package does not render that container** — `components/chat` does, empty at load, and its own comment says the attribute is "a promise to phase 9's script". Rendering it here would give a document with two `[data-chrome="chat-log"]` elements, which `querySelector` resolves to the first, so the reader's log would go nowhere while every hook audit passed.

**Search has no live region, and the assertion is not repeated here.** It is held by `components/chat`'s own test, which renders the search surface and counts its regions — a rule only one route could violate is not a rule, and that is why the assertion lives in a package that owns one of the two surfaces. What this work item adds is that the live chrome is the *only* place a live region is born on a campaign page: `live.AnnouncedContent()` holds §7.5's table as data, every SSE row must name a target this package declares at the region the table says, and every WebSocket row must name none.

### Measured: the frames phase 7 wrote, and the client that reads them

Phase 7 wrote `selector …`, `mode …` and `elements …` as **bare** event-stream field lines, and its own test parsed the frame with a reader that accepted bare lines — so the test asserted the encoder against itself and passed while the product received nothing. The grammar ends a field at the first colon and treats a colon-less line as a field name with an empty value; Datastar's own parser is narrower still and switches on the field name, handling `data`, `event`, `id` and `retry` and ignoring every other name **without complaint**.

Two frames were pushed through a real Chromium against the vendored module, at `mode: inner` and again at `mode: append` with three `elements` lines, the only difference being the prefix:

| Frame | Target afterwards | Console |
|---|---|---|
| `data: selector …`, `data: mode inner`, `data: elements …` | became the fragment, and the three-line append landed as three siblings | empty |
| `selector …`, `mode inner`, `elements …` | **byte-identical** | empty |

The silent half is why this needed a browser rather than a parser. A handler that throws on a bad frame is a frame a reader sees missing and a GM reports; a handler that writes a frame nothing reads is a feature that never arrives, and the only symptom is a page that works.

**Which is why the vendored version is not the newest published one.** Every Datastar release from 0.20.0 to 1.0.0-beta.11 — and 1.0.0-beta.11 is the **last** version npm has — dispatches on `datastar-merge-fragments`. The rename to `datastar-patch-elements` happened at 1.0.0, and 1.0.4 is the newest release (2026-09-21). Pinning the npm `latest` would have shipped a sidebar that never updates, measured rather than read off a changelog, and the same measurement is what caught it. Datastar's own README tells every user to load the bundle from the GitHub tag, and its npm package has not been published since March 2025.

## Consequences

The two connections are countable rather than asserted. `internal/web/static/js/live`'s `TestTheClientOpensExactlyOneWebSocketAndNoSecondTransport` counts the `new WebSocket(` call sites in the shipped bytes and refuses `EventSource`, `fetch`, `XMLHttpRequest` and `sendBeacon`; `components/live`'s `TestTheChromeDeclaresExactlyOneOfEachConnection` counts the two elements in the served document. Both are needed: a document with two `data-init` openers is two streams, and a client opening two sockets is two sockets, and neither is visible from the other. C1's map audit already holds the third side — the map tree opens no transport at all, and says the play page's two connections belong to this chrome.

Adding a sixth target is three edits: a hook constant, a row in `targets()`, and a mount point in `Chrome`. Adding a seventh *kind* is a `Kind` constant, a `Queues()` case, and a row in `AnnouncedContent()` — and the last is a forced decision, because a WebSocket row with a target fails and a state row with no target fails. That is the point of holding the table as data.

The price of the gate move is real and it is stated: a route that was once refused to a player now answers them a stream. What changed is what is *in* it, and `TestTheEditorNoticeIsNotSentToAPlayer` asserts both directions — the player gets nothing, and the GM still gets the notice, because a fix that silenced it for everybody would satisfy the first half.

Three of §7.5's four prohibitions are now refusals or measurements, and the cost is that §10.6's target-size audit over this route's fragments passes **vacuously**: `Decide` refuses any fragment holding a focus stop, so nothing the route emits has one. That is correct behaviour and it is stated in the test's own comment, because the audit would otherwise look like coverage of a rule whose only enforcement is the refusal above it.

The vendor manifest's `source.kind` says `npm` for the Datastar entry and the URL is a GitHub source tarball. That is a lie in a file whose whole purpose is not lying, and it is there because `tools/vendor/main.go` — the integrator's file — refuses any other value. The manifest says so in its own `description` and the work item asks for a third kind rather than writing a manifest no committed reader understands. A reader needs three things from an entry — an `https` URL, a Subresource Integrity digest, and a named member in an archive — and a GitHub tag archive supplies all three identically.

## Alternatives considered

**One connection.** Serving the sidebar over the WebSocket and dropping SSE entirely would have halved the connection count and put every patch behind a codec this repository would then have to specify. Against it: SSE is what §7.5 assigns to the sidebar, `EventSource` reconnects with a `Last-Event-ID` for free, and a browser holds one SSE connection per stream without the client having to implement a heartbeat, a retry ladder and a resume cursor. The cost taken is two connections and a rule about who owns each.

**Structured fragments with a client renderer.** Sending `{"kind":"chat","author":"…","body":"…"}` and letting the client build the row would be smaller on the wire and would let the client update a row in place. Against it: §7.5 requires rendered DOM fragments so the client never renders content, the escaping would become the client's problem, and a chat message body is the one piece of this surface a reader cannot be trusted to have authored. The measurement that decided it is in the context: the escaping would have to be right in two places and the reader would find out about the wrong one.

**Coalesce everything, one buffer, one policy.** Simpler, and it is what phase 7 shipped. Against it: it drops chat lines. Three messages in one second are three messages, and a reader who saw one of them has a transcript with a hole in it that nothing will fill — the same property §7.5's replay rule is protecting on the other side of the connection.

**Filter the editor's notice at the hub rather than in the handler.** The hub could refuse to deliver a `KindChange` to a subscription that had declared itself a player, which would keep the tier out of the patch path. Against it: the hub's subscription is about *which campaign*, and making it about *which reader* means two subscription shapes and two `Subscribe` signatures. The tier travels with the request, which is where it already lives.

**Pin Datastar's npm `latest`.** It is the schema-perfect answer: a registry tarball with a registry integrity string, and no manifest prose to explain. Against it: measured, it does not listen for the event this route writes, and the sidebar would never update. Schema fidelity is worth less than a working live chrome, and the manifest now says which of the two it is buying.