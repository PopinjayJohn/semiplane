// chrome.js — the live chrome's client.
//
// # What this file owns
//
// **The connection, and nothing else.** UI §7.5's second task is "the play page
// opens exactly one WS and one SSE", and this file is the whole of the WS half:
// one `new WebSocket`, opened from the one element that declares the URL, held open
// across a drop, and closed when the page goes away. The SSE half is not ours — the
// vendored Datastar module opens it from the `data-init` on the one element carrying
// `live-events`, so counting it is counting an attribute in the served document
// rather than a constructor call in this file.
//
// # Four things it refuses to do
//
//  1. **Open a second transport.** No `EventSource`, no `fetch`, no
//     `XMLHttpRequest`, no `navigator.sendBeacon`. A second connection per client is
//     a browser connection spent for nothing against plan §7's ceiling of about six,
//     and `source_test.go` fails the build if one appears here.
//
//  2. **Author any English.** §7.5's last row puts "connection lost" in an assertive
//     region, and the sentences for both states are **server-rendered** —
//     `internal/web/components/live`'s `SocketState` renders a connected block and a
//     lost block, one of them `hidden`, into a `role="alert"` container. This file
//     toggles a `hidden` attribute. A client that wrote "Reconnecting…" would be a
//     second place deciding what a lost connection is called, and the two would
//     disagree the first time either was reworded.
//
//  3. **Render content.** It never sets `innerHTML`, never builds an element from a
//     string, and never passes a frame through `eval`. The fragments are the
//     server's, and Datastar places them; this file's whole rendering surface is
//     two attribute writes.
//
//  4. **Reconnect faster than a person could.** `reconnectDelay` doubles from
//     500ms and stops at 30s. A table whose server is restarting reconnects four
//     times and then waits half a minute, which is a backoff and not a loop; a
//     client that reconnected immediately would be a request storm against a process
//     that is already in trouble.
//
// # The DOM contract it expects
//
// Server-rendered elements that `internal/web/components/live` renders. Every one is
// found by its `data-chrome` hook, and the two ids are the hook *values* — one
// spelling, read from the same constants the Go templates use:
//
//     <div hidden data-chrome="live-ws" data-socket="/c/{slug}/ws"></div>
//     <div role="alert" aria-live="assertive">
//       <p data-testid="live-socket-connected">…</p>
//       <p data-testid="live-socket-lost" hidden>…</p>
//     </div>
//
// The first is the socket's URL and is read by this file and by nothing else. The
// second is the notice, and this file moves `hidden` between its two paragraphs.
//
// # The kernel
//
// `spFollowGap`, from `gap.js`, is loaded before this file (see `Order`) and is the
// only arithmetic this file depends on. It decides whether an appended log line
// scrolls into view or leaves the reader where they are — see that file for the rule
// and for why it is arithmetic at all.
//
// The reconnect backoff is **deliberately not** that kernel. It is a doubling
// against a clock, and a Go evaluator that supplied the clock would be a gate on a
// document rather than on the shipped bytes, which is the same reasoning that keeps
// `Math.min` out of `gap.js`.

import { spFollowGap } from "./gap.js";

/** The chrome hooks this module reads, spelled as the Go constants spell them. */
const WS_HOOK = "live-ws";

/** `data-testid` on the two connection-state paragraphs. */
const CONNECTED_TESTID = "live-socket-connected";
const LOST_TESTID = "live-socket-lost";

/** The reconnect backoff: half a second, doubling, capped at half a minute. */
const RECONNECT_BASE_MS = 500;
const RECONNECT_CAP_MS = 30000;

/** The distance below which a scroll region counts as "at the bottom". */
const FOLLOW_TOLERANCE_PX = 4;

/** One controller, because §7.5's task says *one* WS. */
let controller = null;

/**
 * Open the tabletop socket, or return the one already open.
 *
 * Idempotent on purpose: the play document loads this module once, but a route that
 * mounts the chrome into a second shell would otherwise open a second socket, and a
 * second socket is a connection nobody can account for from the browser's own
 * connection list.
 *
 * @param {ParentNode} [root] where to look for the hook; the document by default
 * @returns {object|null} the controller, or null when the chrome is not mounted
 */
export function mountLiveChrome(root) {
  const scope = root || document;

  if (controller !== null) {
    return controller;
  }

  const hook = scope.querySelector(`[data-chrome="${WS_HOOK}"]`);
  if (hook === null) {
    // Not an error. A document without the chrome has no table to watch, and a
    // module that threw here would stop every other module on the page with it.
    return null;
  }

  controller = new LiveController(hook);
  controller.open();

  return controller;
}

/** The connection's state, and the two things that depend on it. */
class LiveController {
  constructor(hook) {
    this.hook = hook;
    this.attempt = 0;
    this.timer = null;
    this.socket = null;
  }

  /** The URL, read from the hook. Read once, because the document does not change. */
  get url() {
    return this.hook.getAttribute("data-socket");
  }

  /** Open the socket, or schedule the next attempt. */
  open() {
    if (this.url === null || this.url === "") {
      return;
    }

    this.socket = new WebSocket(this.url);

    this.socket.addEventListener("open", () => {
      this.attempt = 0;
      this.announce(true);
    });

    this.socket.addEventListener("message", (event) => {
      this.dispatch(event.data);
    });

    this.socket.addEventListener("close", () => {
      this.announce(false);
      this.schedule();
    });

    // A failed handshake is a `close`, and reconnecting on both would open two
    // sockets per drop.
    this.socket.addEventListener("error", () => {});
  }

  /** Stop reconnecting and close the socket. */
  close() {
    if (this.timer !== null) {
      clearTimeout(this.timer);
      this.timer = null;
    }

    if (this.socket !== null) {
      this.socket.close();
      this.socket = null;
    }
  }

  /** Wait `reconnectDelay(attempt)` and try again. */
  schedule() {
    this.attempt += 1;

    this.timer = setTimeout(() => {
      this.timer = null;
      this.open();
    }, reconnectDelay(this.attempt, RECONNECT_BASE_MS, RECONNECT_CAP_MS));
  }

  /**
   * Move the connection notice.
   *
   * A `hidden` attribute and nothing else — see the file header's second refusal.
   * The paragraphs are found by their test hook rather than by a selector over the
   * chrome's children, so a panel added between them cannot break the toggle.
   */
  announce(connected) {
    const ok = this.hook.ownerDocument.querySelector(
      `[data-testid="${CONNECTED_TESTID}"]`,
    );
    const lost = this.hook.ownerDocument.querySelector(
      `[data-testid="${LOST_TESTID}"]`,
    );

    if (ok !== null) {
      ok.hidden = !connected;
    }

    if (lost !== null) {
      lost.hidden = connected;
    }
  }

  /**
   * Hand one frame on.
   *
   * **A `CustomEvent`, not a renderer.** Architecture §7.1's server frames are
   * structured state for the canvas, and the canvas's consumer is the map module —
   * which this work item does not own and which explicitly expects to be handed state
   * rather than to go looking for it (`table.js`: "State arrives by
   * `handle.setState`"). Dispatching on the element that declared the hook gives
   * every consumer one subscription point, and the event name is this repository's
   * own rather than a new one invented for two modules to agree on.
   *
   * The frame is passed through **unparsed**. A frame this file cannot understand is
   * still a frame the map can, and a client that tried to parse the protocol would be
   * a second implementation of `internal/realtime`'s codec — the same duplication
   * `internal/httpapi/events` refuses when it writes Datastar's fields by hand.
   */
  dispatch(raw) {
    this.hook.dispatchEvent(
      new CustomEvent("sp:frame", { detail: { raw } }),
    );
  }
}

/**
 * Should a log scroll this line into view?
 *
 * @param {Element} region the scrolling list
 * @returns {boolean} true when the reader was already at the bottom
 */
export function followsTail(region) {
  return (
    spFollowGap(
      region.scrollTop,
      region.clientHeight,
      region.scrollHeight,
      FOLLOW_TOLERANCE_PX,
    ) >= 0
  );
}

/** The reconnect wait for an attempt, doubling from `base` up to `cap`. */
export function reconnectDelay(attempt, base, cap) {
  const scaled = base;
  for (let step = 1; step < attempt; step += 1) {
    if (scaled >= cap) {
      return cap;
    }

    scaled = scaled + scaled;
  }

  return scaled >= cap ? cap : scaled;
}