// camera.js — the map camera, as arithmetic over plain numbers.
//
// # Why this file is written in a strange, restricted style
//
// It is not a general JavaScript module. **Every function here is a single
// `return` of one expression** over numbers, records of numbers, and a closed set
// of built-in functions. There is no `if`, no `for`, no `let`, no string, no
// `document`, and no branching of any kind: `&&` and `||` are here because a
// conjunction of two numbers is arithmetic, not control flow, and neither side of
// either can have an effect.
//
// **Those built-ins are spelled exactly as JavaScript spells them — `Math.min`,
// not `min`.** That is not a style choice, and this file has the scar to prove it.
// It shipped once with a bare `min(…)`; the Go evaluator supplies a `min`, so every
// test in the suite evaluated it, `make check` was green, and a browser would have
// thrown `ReferenceError` on the first frame. A gate that invents a name its host
// language does not have is not a gate on the shipped bytes — it is a gate on a
// document. The evaluator's closed vocabulary therefore *is* `Math`'s, and
// `TestNoCameraFunctionReadsAnythingItWasNotGiven` is what now catches the mistake:
// a bare `min` is a name nothing in JavaScript or in the evaluator binds.
//
// That restriction is not taste. It is what makes the file *checkable by the Go
// gate*: `camera_arith_test.go` parses this file with a small expression
// evaluator and evaluates the **shipped bytes** — exhaustively, over grids of
// bounds, viewports, scales and map points. No transcription, no second
// implementation to drift, and no Node on the CI runner.
//
// The failure mode the restriction exists to prevent is the one this repository
// keeps meeting: a test that passes because it is reading something other than
// the thing that ships. If a future edit adds a conditional, the evaluator
// **refuses to parse the file and the build fails loudly** — the design property
// is that "unsupported" means "red", never "blind". `TestTheArithmeticGate
// RejectsWhatItCannotCheck` feeds the evaluator a conditional, a loop, a string
// literal, a call through a name it does not bind, and an assignment, and requires it
// to object to each.
//
// # The model, and the one invariant everything else serves
//
// A camera is **three numbers**:
//
//   - `centreX`, `centreY` — a point in **map coordinate units**, which are the
//     scene image's own pixel grid. Not screen pixels, not CSS pixels.
//   - `worldPerViewport` — **map units per viewport unit**: how much of the map one
//     viewport unit covers. This is the requirement's own unit, and it is the
//     *inverse* of a zoom factor — a zoomed-in camera has a **smaller** value. So a
//     resize, which makes each viewport unit cover fewer map units, cannot be
//     expressed by this field at all without something else to hold the pixels.
//
// There is no fourth field, and there is no zoom in pixels anywhere in this file.
// That is UI §7.6's "zoom is expressed in world coordinate units, never pixels",
// and it is load-bearing because of what it makes *unrepresentable*: a camera that
// remembered "at 1200 viewport units wide the map looked right" would have to
// re-derive itself on every resize. A camera that cannot name a pixel cannot
// re-frame.
//
// So the viewport is **not an input to the camera's identity**. `resizeCamera`
// takes the new viewport and returns the same camera, and it deliberately takes
// **no map bounds** — so there is nothing in scope it could re-fit from. The
// evaluator knows every function's parameter list and
// `TestTheCameraCannotBeRefitFromAViewport` asserts it, so "resize cannot
// re-frame" is a property of a signature rather than a promise in a comment.
//
// The projection, everywhere in this file, is:
//
//   screen = (map - centre) / worldPerViewport + viewport / 2
//
// `resizeCamera` is the identity on those three fields, which means a map point
// keeps the same **fractional** position in the viewport across a resize or an
// orientation change. That fraction is the invariant
// `TestTheMapSurvivesAResizeWithoutReframing` asserts: project a map point through
// the old camera, scale by the viewport ratio, invert through the new one, and
// require the original point back.
//
// # What is deliberately absent
//
// There is **no clamp of the camera to the map's edges**. Clamping would move the
// centre on resize — a narrow viewport clamping a wide map is exactly the
// re-framing this file exists to prevent — so the map is *letterboxed* instead,
// which is what UI §4.9 asks for at compact anyway. `clampCamera` clamps the
// **scale**, the other thing a user can drive out of range, and `zoomAt` applies
// it *before* re-centring, so clamping moves the focus rather than the map.

/** The camera that fits `map` inside `viewport` with the whole scene visible.
 *
 * `worldPerViewport` is the smaller of the two axis ratios, so the scene is
 * contained rather than cropped and the surplus becomes the letterbox band.
 *
 * Called **once per scene**, from `mount` in `table.js`, and never from a resize —
 * which is the whole reason `resizeCamera` exists separately.
 */
export function fitCamera(map, viewport) {
  return {
    centreX: map.width / 2,
    centreY: map.height / 2,
    worldPerViewport: Math.max(map.width / viewport.width, map.height / viewport.height)
  };
}

/** The camera after the viewport changed size or orientation.
 *
 * Returns the same camera. The parameters are `camera` and `viewport` and nothing
 * else: no map bounds reach this function, so re-framing is not available to it
 * even by accident.
 */
export function resizeCamera(camera, viewport) {
  return {
    centreX: camera.centreX,
    centreY: camera.centreY,
    worldPerViewport: camera.worldPerViewport
  };
}

/** Project a map point to a viewport point, in viewport units. */
export function worldToScreen(camera, viewport, point) {
  return {
    x: (point.x - camera.centreX) / camera.worldPerViewport + viewport.width / 2,
    y: (point.y - camera.centreY) / camera.worldPerViewport + viewport.height / 2
  };
}

/** Project a viewport point to a map point — the inverse of `worldToScreen`. */
export function screenToWorld(camera, viewport, screen) {
  return {
    x: (screen.x - viewport.width / 2) * camera.worldPerViewport + camera.centreX,
    y: (screen.y - viewport.height / 2) * camera.worldPerViewport + camera.centreY
  };
}

/** The scale a PixiJS container needs to reproduce `worldToScreen`.
 *
 * PixiJS maps a child's local point to `position + scale * point`, and the projection
 * is `screen = (map - centre) / worldPerViewport + viewport / 2`, so the container's
 * scale is the **reciprocal** of the camera's.
 *
 * **It is a function because the reciprocal is exactly what gets written the wrong
 * way round**, and getting it wrong is invisible from a distance: the map still
 * renders, still pans and still zooms, and the placements are drawn at twelve times
 * their size with their labels off screen. It was found by screenshotting a mounted
 * map, not by any test — so `TestTheContainerTransformIsTheProjection` now projects a
 * point through this scale and `containerOffset` and compares against
 * `worldToScreen`, and the reciprocal is on the side the gate can read.
 */
export function containerScale(camera) {
  return 1 / camera.worldPerViewport;
}

/** The position a PixiJS container needs, so its origin lands at the viewport's middle.
 *
 * The two halves of the projection again: the viewport's half-size is where the
 * camera's centre lands, less the centre expressed in viewport units.
 */
export function containerOffset(camera, viewport) {
  return {
    x: viewport.width / 2 - camera.centreX / camera.worldPerViewport,
    y: viewport.height / 2 - camera.centreY / camera.worldPerViewport
  };
}

/** The map rectangle currently inside the viewport, in map units. */
export function visibleWorld(camera, viewport) {
  return {
    left: camera.centreX - (viewport.width * camera.worldPerViewport) / 2,
    top: camera.centreY - (viewport.height * camera.worldPerViewport) / 2,
    right: camera.centreX + (viewport.width * camera.worldPerViewport) / 2,
    bottom: camera.centreY + (viewport.height * camera.worldPerViewport) / 2
  };
}

/** The same camera, centred on `point`.
 *
 * This is the token list's "select and move the camera" (UI §7.6), driven by a
 * `<button>` — which is why the canvas needs no focus of its own. It changes only
 * the centre, so it cannot change what the map is zoomed to.
 */
export function centreOn(camera, point) {
  return {
    centreX: point.x,
    centreY: point.y,
    worldPerViewport: camera.worldPerViewport
  };
}

/** The camera panned by a pointer drag of `delta` viewport units.
 *
 * A drag moves the map with the pointer, so the centre moves the other way by the
 * drag expressed in map units.
 */
export function panScreen(camera, delta) {
  return {
    centreX: camera.centreX - delta.x * camera.worldPerViewport,
    centreY: camera.centreY - delta.y * camera.worldPerViewport,
    worldPerViewport: camera.worldPerViewport
  };
}

/** The camera with its scale held between the two bounds.
 *
 * `nearest` is one map unit per viewport unit — 1:1, past which a map is only
 * interpolation — and `widest` is the fit scale for the current viewport, past which
 * the map is smaller than the surface it is drawn on.
 *
 * **Either order.** `min` and `max` are applied to the two bounds before the value
 * is clamped, because which of the two is larger depends on the scene: a 256-unit
 * map fits a 1280-unit viewport at 0.2, so the fit is the *magnified* end and 1:1
 * is the zoomed-out end; a 4096-unit map on the same viewport fits at 3.2, so the
 * fit is the zoomed-out end and 1:1 is inside it. A clamp written for one of those
 * orders silently pins the scale for the other, which is a map that cannot be
 * zoomed at all and looks like a broken wheel.
 */
export function clampCamera(camera, nearest, widest) {
  return {
    centreX: camera.centreX,
    centreY: camera.centreY,
    worldPerViewport: Math.max(Math.min(nearest, widest), Math.min(Math.max(nearest, widest), camera.worldPerViewport))
  };
}

/** The camera that holds the map point under `focus` still under `focus`.
 *
 * `factor` above 1 zooms in, which means **fewer** map units per viewport unit, so
 * `worldPerViewport` is divided by `factor`. The clamp is applied **before**
 * `recentreOnFocus`, so a zoom that hits a bound re-centres on where the pointer
 * actually is at the clamped scale rather than jumping.
 */
export function zoomAt(camera, viewport, focus, factor, nearest, widest) {
  return recentreOnFocus(
    camera,
    {
      centreX: camera.centreX,
      centreY: camera.centreY,
      worldPerViewport: Math.max(Math.min(nearest, widest), Math.min(Math.max(nearest, widest), camera.worldPerViewport / factor))
    },
    viewport,
    focus
  );
}

/** The scaled camera re-centred so the map point under `focus` stays under it.
 *
 * From the projection, `world = centre + (screen - viewport/2) * worldPerViewport`, a
 * point under a fixed screen position satisfies
 * `centre = point - (focus - viewport/2) * s`. Subtracting that for the two cameras
 * gives the shift:
 *
 *     after.centre = before.centre + (focus - viewport/2) * (before.s - after.s)
 *
 * **The form is the load-bearing part.** When a zoom hits a bound and `after.s`
 * equals `before.s`, the difference is exactly zero and the camera does not move —
 * arithmetically, with no conditional to get wrong. A re-centre written as "solve
 * for the centre that puts the focus point under the cursor" moves the map every
 * time the reader spins the wheel at maximum zoom, which on a small map slings it
 * off the screen; `TestZoomHoldsThePointUnderThePointer` is what caught that.
 */
export function recentreOnFocus(before, after, viewport, focus) {
  return {
    centreX: before.centreX + (focus.x - viewport.width / 2) *
      (before.worldPerViewport - after.worldPerViewport),
    centreY: before.centreY + (focus.y - viewport.height / 2) *
      (before.worldPerViewport - after.worldPerViewport),
    worldPerViewport: after.worldPerViewport
  };
}

/** A map length expressed in map units so that it draws `screenPixels` wide.
 *
 * This is the conversion behind both 3px requirements in UI §7.6: the placement
 * outline, the grid line and the in-map label are all fixed **screen** sizes that
 * have to be drawn in a map-unit coordinate space. The camera multiplies a map
 * length by `worldPerViewport`, so a screen length divides by it.
 *
 * `TestTheOutlineIsThreeScreenPixelsAtEveryScale` is the arithmetic half of that
 * claim and `TestTheMapSourceHoldsTheContrastAndAccessibilityContract` is the half
 * that says the shipped scene actually asks for 3.
 */
export function worldWidthForScreenPixels(camera, screenPixels) {
  return screenPixels * camera.worldPerViewport;
}

/** A screen size in viewport units, scaled by `--type-scale`.
 *
 * UI §7.6: "map labels inside the canvas scale with `--type-scale`". The scale is
 * read from the document by `palette.js` and arrives here as a plain number, so a
 * label is bigger on a television without any of this file knowing what a
 * television is.
 *
 * Kept separate from `labelWorldSize` because the initiative strip is drawn in
 * **screen** space — it is chrome over the map, not part of it — and so needs the
 * same scale without the camera's division.
 */
export function screenSize(baseScreenPixels, typeScale) {
  return baseScreenPixels * typeScale;
}

/** The map-unit font size for a label of `baseScreenPixels` at `typeScale`.
 *
 * Screen pixels multiplied by how many map units a viewport unit covers, so the
 * label lands at exactly `baseScreenPixels * typeScale` on screen at every zoom —
 * the property `TestLabelsHoldTheirScreenSizeAtEveryScale` asserts.
 */
export function labelWorldSize(camera, baseScreenPixels, typeScale) {
  return screenSize(baseScreenPixels, typeScale) * camera.worldPerViewport;
}

/** The distance between two map points, in map units. */
export function mapDistance(a, b) {
  return Math.sqrt((a.x - b.x) * (a.x - b.x) + (a.y - b.y) * (a.y - b.y));
}

/** Whether two map-space rectangles overlap, touching edges included. */
export function overlaps(a, b) {
  return a.right >= b.left && a.left <= b.right && a.bottom >= b.top && a.top <= b.bottom;
}

/** Whether a map-space rectangle overlaps what the camera shows.
 *
 * `scene.js` asks this per placement before it draws, so a table with two hundred
 * placements draws the dozen that are on screen.
 */
export function isVisible(camera, viewport, rect) {
  return overlaps(rect, visibleWorld(camera, viewport));
}