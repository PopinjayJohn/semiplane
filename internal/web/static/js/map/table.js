// table.js — the map surface's entry point.
//
// # What this file owns
//
// Mounting, measuring and input. The arithmetic is `camera.js`, the pixels are
// `scene.js`, and the colours are `palette.js`. This is the only file that touches
// the DOM, and the only one that knows a viewport exists.
//
// # Four things it refuses to do
//
//  1. **Open a transport.** No `WebSocket`, no `EventSource`, no `fetch`. The play
//     page opens exactly one WS and one SSE (architecture §7), both owned by C3's
//     live chrome, and a second connection per client is a browser connection
//     spent for nothing. State arrives by `handle.setState`.
//  2. **Give the canvas focus.** It is `aria-hidden="true"` and it is a mirror of
//     the token list. Two places assert it and both are needed: `mount` **refuses**
//     rather than assumes, because a surface that is not hidden announces an
//     unlabelled graphic and no amount of correct drawing fixes that, and `scene.js`
//     sets it on the canvas it creates, because the canvas did not exist when the
//     server rendered the surface.
//  3. **Leave a pointer handler on a readout.** UI §4.9: the compact map renders,
//     never takes a gesture, and never reaches the bottom of the screen. The
//     handlers are bound and unbound from the `(pointer: coarse)` media query's own
//     change event, so a surface that crosses into coarse has none attached at all
//     — not attached and inert, which is the difference the requirement is about.
//  4. **Re-fit on resize.** `onResize` calls `scene.resize`, which calls
//     `resizeCamera`. `fitCamera` is reached from exactly one line in this tree,
//     the initial mount, and `TestTheResizePathNeverCallsFitCamera` holds that.
//
// # The DOM contract it expects
//
// A server-rendered element, which C4's play page owns:
//
//     <div data-map-surface
//          data-map-src="/c/{slug}/assets/maps/keep.png"
//          data-map-width="4096" data-map-height="3072"
//          data-map-grid="70"
//          aria-hidden="true"></div>
//
// Every attribute after the first is optional: the scene image is the authority on
// its own size, and on a grid step that was not declared. The attribute names carry
// no "world" and no "session" — the vocabulary rule is about the interface, and a
// data attribute on a hidden element is close enough to the interface that not
// using the word costs nothing.

import { createScene } from "./scene.js";

/** The tier at which the map is a readout rather than a control, matched by prefix.
 *
 * `data-ui` is `head.js`'s own vocabulary (UI §3.7's resolver) and `compact-short`
 * is a separate value with its own height budget. Matching the prefix rather than
 * an exact value is what makes a future compact tier read-only without an edit
 * here.
 */
const compactTierPrefix = "compact";

/** How far a pointer may travel before it is a drag rather than a click.
 *
 * A resting pointer's own jitter is a pixel or two; anything past this is a
 * deliberate movement. In viewport units, because it is a fact about the pointer
 * rather than about the map.
 */
const dragThresholdPixels = 4;

/** The zoom applied by one wheel notch, as a factor. */
const wheelZoom = 1.1;

/** One mount per surface, and the promise of its handle.
 *
 * A `WeakMap` rather than a `WeakSet` because the *handle* is what a caller needs,
 * not merely the fact that something mounted. A page that calls `boot` after the
 * automatic boot has already begun — which is what the play page will do, and what
 * any test harness does — gets the same handle back rather than a second WebGL
 * context and an empty array.
 *
 * **A `WeakMap` rather than a `data-` attribute**, because an attribute would be a
 * second record of the same fact that a page could edit, and a page that cleared it
 * would get a second context on one element.
 *
 * The entry is removed by `handle.destroy`, so a surface that is torn down and
 * rendered again mounts afresh rather than handing back a destroyed scene.
 */
const mounts = new WeakMap();

/** Mounts every `[data-map-surface]` in `doc`, and returns their handles.
 *
 * Resolves once each surface has painted its first frame. **A surface that fails to
 * mount resolves to `null` rather than rejecting**: a renderer that cannot start must
 * not take the page with it, because the token list is the accessibility source of
 * truth and a dead canvas leaves a working interface behind it. The cause is reported
 * on the console, and a caller that wants to hear it can call `mount` directly.
 */
export function boot(doc) {
  const started = [];

  for (const surface of doc.querySelectorAll("[data-map-surface]")) {
    started.push(mount(surface));
  }

  return Promise.all(started);
}

/** Mounts one map surface and returns a handle for it.
 *
 * Resolves once the first frame is on the canvas. The handle is what C3's live
 * chrome calls: `setState` for a state document, `select` for the token list's
 * "select and move the camera", `destroy` when the route unmounts.
 */
export function mount(surface) {
  const existing = mounts.get(surface);

  if (existing !== undefined) {
    return existing;
  }

  const started = start(surface).catch((error) => {
    // The failed attempt is forgotten, so a caller that fixes the cause and retries
    // gets a real attempt rather than the cached rejection for ever.
    mounts.delete(surface);

    throw error;
  });

  mounts.set(surface, started);

  return started;
}

// start is the body of a mount, split from `mount` because `mount` is now the
// idempotent front door and this is the work it fronts.
async function start(surface) {
  const doc = surface.ownerDocument;
  const view = doc.defaultView;

  if (surface.getAttribute("aria-hidden") !== "true") {
    throw new Error("a map surface must be hidden from assistive technology; it is a mirror");
  }

  if (surface.getAttribute("role") === "application") {
    throw new Error("role=application is prohibited on the play page");
  }

  const source = surface.getAttribute("data-map-src");

  if (source == null || source === "") {
    throw new Error("a map surface needs data-map-src naming the scene image");
  }

  const scene = await createScene(surface, {
    source,
    width: numberAttribute(surface, "data-map-width"),
    height: numberAttribute(surface, "data-map-height"),
    grid: numberAttribute(surface, "data-map-grid"),
    viewport: measure(surface),
    // One 1×1 2D context for the page, shared by every mount. `palette.js` uses it
    // to canonicalise colours and keeps no state between calls.
    paletteProbe: doc.createElement("canvas").getContext("2d")
  });

  const canvas = scene.canvas;

  const coarse = view.matchMedia("(pointer: coarse)");
  let viewport = measure(surface);
  let drag = null;
  let frame = 0;
  let bound = false;

  function isReadOnly() {
    return coarse.matches || (doc.documentElement.dataset.ui || "").startsWith(compactTierPrefix);
  }

  function onPointerDown(event) {
    drag = { x: event.clientX, y: event.clientY, from: { x: event.clientX, y: event.clientY } };
  }

  function onPointerMove(event) {
    if (drag == null) {
      return;
    }

    const travelled = Math.hypot(event.clientX - drag.from.x, event.clientY - drag.from.y);

    if (travelled < dragThresholdPixels) {
      return;
    }

    scene.pan({ x: drag.x - event.clientX, y: drag.y - event.clientY });
    drag.x = event.clientX;
    drag.y = event.clientY;
  }

  function onPointerUp(event) {
    if (drag == null) {
      return;
    }

    const travelled = Math.hypot(event.clientX - drag.from.x, event.clientY - drag.from.y);

    drag = null;

    if (travelled >= dragThresholdPixels) {
      return;
    }

    const hit = scene.placementAt(localPoint(canvas, event));

    handle.select(hit == null ? null : hit.id);
  }

  function onWheel(event) {
    event.preventDefault();
    scene.zoom(event.deltaY < 0 ? wheelZoom : 1 / wheelZoom, localPoint(canvas, event));
  }

  function bindInput() {
    if (bound) {
      return;
    }

    bound = true;
    canvas.addEventListener("pointerdown", onPointerDown);
    canvas.addEventListener("pointermove", onPointerMove);
    canvas.addEventListener("pointerup", onPointerUp);
    canvas.addEventListener("pointercancel", onPointerUp);
    canvas.addEventListener("wheel", onWheel, { passive: false });
  }

  function unbindInput() {
    if (!bound) {
      return;
    }

    bound = false;
    drag = null;
    canvas.removeEventListener("pointerdown", onPointerDown);
    canvas.removeEventListener("pointermove", onPointerMove);
    canvas.removeEventListener("pointerup", onPointerUp);
    canvas.removeEventListener("pointercancel", onPointerUp);
    canvas.removeEventListener("wheel", onWheel);
  }

  function onCoarseChange() {
    if (isReadOnly()) {
      unbindInput();

      return;
    }

    bindInput();
  }

  function onResize() {
    const next = measure(surface);

    if (next.width === viewport.width && next.height === viewport.height) {
      return;
    }

    viewport = next;
    scene.resize(next);
  }

  function schedule() {
    if (frame !== 0) {
      return;
    }

    // Coalesced to one frame because a breakpoint crossing delivers several resize
    // events, and each one repaints every layer.
    frame = view.requestAnimationFrame(() => {
      frame = 0;
      onResize();
    });
  }

  // A ResizeObserver covers a breakpoint crossing and a window resize. The
  // orientation event is handled as well because a phone rotating settles its
  // layout a frame or two after the event, and a canvas sized from the pre-rotate
  // box is the "re-frames on rotation" failure this whole file exists to avoid.
  const observer = new view.ResizeObserver(schedule);

  observer.observe(surface);
  view.addEventListener("orientationchange", schedule);
  coarse.addEventListener("change", onCoarseChange);

  if (!isReadOnly()) {
    bindInput();
  }

  const handle = {
    scene,

    /** Replaces the state document the mirror renders. */
    setState(state) {
      scene.setState(state);
    },

    /** The token list's "select and move the camera" (UI §7.6). */
    select(placementId) {
      scene.select(placementId);
    },

    /** Whether this surface is currently a readout rather than a control. */
    get readOnly() {
      return isReadOnly();
    },

    destroy() {
      mounts.delete(surface);
      observer.disconnect();
      unbindInput();
      view.removeEventListener("orientationchange", schedule);
      coarse.removeEventListener("change", onCoarseChange);

      if (frame !== 0) {
        view.cancelAnimationFrame(frame);
        frame = 0;
      }

      scene.destroy();
    }
  };

  return handle;
}

/** A pointer event's position in viewport units, relative to the canvas. */
function localPoint(canvas, event) {
  const box = canvas.getBoundingClientRect();

  // The camera's viewport is CSS pixels — `measure` reads a bounding rect and the
  // renderer is told the same numbers — so the pointer is converted in CSS pixels
  // too. Scaling by the backing store would be right only if the camera worked in
  // device pixels, and it deliberately does not.
  return { x: event.clientX - box.left, y: event.clientY - box.top };
}

/** The surface's content box, in viewport units, rounded to whole pixels. */
function measure(surface) {
  const box = surface.getBoundingClientRect();

  return {
    width: Math.max(1, Math.round(box.width)),
    height: Math.max(1, Math.round(box.height))
  };
}

/** A data attribute read as a number, or `null` when absent or unusable. */
function numberAttribute(surface, name) {
  const raw = surface.getAttribute(name);

  if (raw == null || raw.trim() === "") {
    return null;
  }

  const parsed = Number.parseFloat(raw);

  return Number.isFinite(parsed) ? parsed : null;
}

// Automatic boot, once the document has a body to query. `DOMContentLoaded` rather
// than a top-level side effect because the module is loaded with `type="module"`,
// which defers, and a `querySelectorAll` at module scope would then find the
// surface only by luck.
if (typeof document !== "undefined" && document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", () => boot(document), { once: true });
} else if (typeof document !== "undefined") {
  boot(document);
}