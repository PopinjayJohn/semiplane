// scene.js — what the canvas draws.
//
// # The layer order is the contrast decision
//
// `world` holds, back to front:
//
//     sceneSprite → fog → dim → grid → placements
//
// `dim` sits **over the artwork and the fog and under the grid and the
// placements**, and that position is the whole argument. UI §7.6's rule is
// "`--map-dim` on the map layer, 3px `--map-outline` on every placement and grid
// line", and `tokens.css` says why the two tokens are not interchangeable:
// `--map-dim` is translucent and therefore carries no luminance guarantee at all,
// while `--map-outline` is an alias of `--text-subtle` and is the token that has to
// clear 3:1. A scrim drawn *over* the outlines would dim the one thing on the map
// that is required to be perceivable, so it is drawn under them instead.
//
// Every outline this file strokes is 3 **screen** pixels wide regardless of zoom.
// `worldWidthForScreenPixels` in `camera.js` is the conversion, and it exists
// because a constant width in map units would be 1px at one zoom and 30px at
// another — which is the difference between a contrast overlay and a decoration.
//
// # Nothing here is reachable, and nothing here is the interface
//
// `app.canvas` is `aria-hidden="true"`, and `table.js` asserts that before
// anything is built. Every value painted here — a name, a position, whose turn it
// is — is also in the token list, which is C2's file and the accessibility source
// of truth. So the labels below are *mirrors*: they exist so a sighted reader does
// not have to move a pointer to read the map, and they are never the only copy of
// anything.
//
// The consequence for this file is that it renders from the state it is given and
// **invents nothing**. In particular it derives no turn order and no fog of its
// own, because a client that computed either would be a second answer to a
// question the server owns — UI §4.9 says "flag it, do not invent a client-side
// turn flag", and this file is that flag.
//
// # The feed this expects
//
// `setState` takes `realtime`'s state document verbatim — `{revision, paused,
// placements}` — and reads two **additional, provisional** keys when present:
//
//   - `fog`: `[{ left, top, right, bottom, state }]` in map units, `state` being
//     `"hidden"`, `"remembered"` or `"visible"`.
//   - `initiative`: placement ids in turn order, and `activePlacement` naming the
//     one whose turn it is.
//
// **Neither is on the wire today.** `realtime.Document` carries `revision`,
// `paused` and `placements` and nothing else, so both are absent, both layers are
// skipped, and the map is placements over artwork. That is the correct
// degradation, and it is why the shapes are documented here rather than invented
// in the renderer: adding them is a protocol change in `internal/realtime`, which
// this work item does not own.

import {
  Application,
  Assets,
  Container,
  Graphics,
  Sprite,
  Text
} from "../../vendor/pixi.min.mjs";
import * as camera from "./camera.js";
import { fadeTint, readPalette } from "./palette.js";

/** Screen-pixel sizes, converted to map units each frame by `camera.js`.
 *
 * `outlinePixels` is UI §7.6's 3px. `labelPixels` is the base label size before
 * `--type-scale` multiplies it, and `markerPixels` is the initiative marker beside
 * its number.
 */
const outlinePixels = 3;
const labelPixels = 12;
const markerPixels = 36;

/** The near zoom bound: one map unit per viewport unit.
 *
 * The far bound is not a constant — it is the fit scale for the *current* viewport,
 * so a map may not be zoomed out past fitting. That is the letterbox edge, and going
 * past it makes the map smaller than its own surface for no reason. It is recomputed
 * per zoom because it depends on the viewport, which is exactly the dependency
 * `resizeCamera` is careful not to have — and `clampCamera` accepts the two bounds in
 * either order, because for a small map the fit is the magnified end and 1:1 is
 * inside it rather than outside.
 */
const nearestWorldPerViewport = 1;

/** The three fog steps, as fractions of `--map-dim`'s own alpha.
 *
 * **This is a dim, not an occlusion, and the difference is a missing token.**
 * Hiding unexplored artwork needs an opaque fill, and the layer has none: `--map-dim`
 * is the only translucent token, and the two candidates for an opaque one are wrong in
 * opposite directions — `--surface` is near-white in light, so unexplored areas came out
 * *brighter* than the map, and `--surface-sunken` is near-white in light too. A fog of
 * war that is brighter than the scene is a fog of war nobody asked for.
 *
 * So the ladder is `--map-dim` alone, which darkens progressively in both themes and
 * never brightens, and **an opaque fog token is reported as missing** rather than
 * invented. C5's theme layer owns the token list; until it has one, a GM's players see
 * dimmed unexplored terrain instead of nothing.
 */
const fogSteps = { hidden: 1, remembered: 0.45, visible: 0 };

/** Builds the renderer and everything it owns.
 *
 * `surface` is the element the server rendered for the map. `options` carries the
 * scene's source URL and its size in map units (read from that element's data
 * attributes by `table.js`), the grid step in map units, the current viewport, and
 * a 2D canvas context shared with `palette.js` for colour normalisation.
 */
export async function createScene(surface, options) {
  const palette = readPalette(surface, options.paletteProbe);

  if (palette["map-dim"] == null || palette["map-outline"] == null) {
    // A refusal rather than a substitute. A canvas painting its own idea of the
    // dim colour is a state `tokens_contrast_test.go` has never measured.
    throw new Error(
      "the map surface resolved no --map-dim or --map-outline, so the shell stylesheet did not load"
    );
  }

  const texture = await Assets.load(options.source);
  const app = new Application();

  await app.init({
    width: options.viewport.width,
    height: options.viewport.height,
    antialias: true,
    autoDensity: true,
    resolution: surface.ownerDocument.defaultView.devicePixelRatio || 1,
    // WebGL, not WebGPU. A television is the target UI §1.3 calls out and most of
    // them ship no WebGPU at all, so a build that prefers it spends its first
    // frames failing to get a device. `webgl` is a one-word change if the fleet
    // ever gains one, and it is a one-word change here rather than in every
    // deployment.
    preference: "webgl",
    backgroundAlpha: 0
  });

  // The scene's own pixel grid is the map unit, so an absent or nonsensical
  // declared size falls back to the image's: the image is the authority on how
  // big it is. Likewise a grid, because a GM who has declared no grid is still
  // describing a map the renderer can lay out.
  const map = {
    width: positive(options.width, texture.width),
    height: positive(options.height, texture.height)
  };
  const grid = positive(options.grid, 70);

  // The canvas is appended here rather than handed back for the caller to place,
  // because **the two attributes on it are the same claim as the order of the two
  // statements**: a canvas that is in the document is announced, and a canvas that is
  // announced is a mirror pretending to be the interface. Marking it before it is
  // reachable, in the module that created it, is what makes "hidden" a property of
  // construction rather than a line somebody might delete.
  const canvas = app.canvas;
  canvas.setAttribute("aria-hidden", "true");
  canvas.removeAttribute("role");
  surface.append(canvas);

  // `world` carries the entire camera transform; `hud` is screen-space chrome over
  // it. The stage is a plain container, so a resize is one assignment rather than
  // a walk of the tree.
  const world = new Container();
  const hud = new Container();
  app.stage.addChild(world, hud);

  const sceneSprite = new Sprite(texture);
  sceneSprite.width = map.width;
  sceneSprite.height = map.height;

  const fogLayer = new Graphics();
  const dimLayer = new Graphics();
  const gridLayer = new Graphics();
  const placements = new Container();
  world.addChild(sceneSprite, fogLayer, dimLayer, gridLayer, placements);

  const strip = new Graphics();
  const stripLabels = new Container();
  hud.addChild(strip, stripLabels);

  // The scrim covers the scene rather than the viewport, so it tracks the map as
  // the camera moves and never darkens the letterbox band.
  dimLayer.rect(0, 0, map.width, map.height).fill(palette["map-dim"]);

  const views = new Map();
  let state = {};
  let cameraState = camera.fitCamera(map, options.viewport);
  let selected = null;
  let destroyed = false;

  function applyCamera() {
    // Both halves of the camera, and neither is written out here: `camera.js` owns
    // the projection, so the arithmetic that has to agree with `worldToScreen` lives
    // in one place and the gate can hold it.
    //
    // The offset's two fields are read out rather than handing the record to
    // `position.set`. PixiJS's `set` takes either two numbers or a `PointData`, and a
    // plain `{x, y}` is right — but handing it the record silently stored the whole
    // object in `position.x`, so the world was translated by an object, drew nothing,
    // and reported no error anywhere. Reading two numbers cannot go wrong that way.
    const offset = camera.containerOffset(cameraState, options.viewport);

    world.scale.set(camera.containerScale(cameraState));
    world.position.set(offset.x, offset.y);
  }

  function redrawGrid() {
    const seen = camera.visibleWorld(cameraState, options.viewport);

    gridLayer.clear();

    // Lines land on multiples of the grid step measured from the map's own origin,
    // not from the visible edge, so the grid stays put while the camera pans
    // instead of crawling with it.
    const fromX = Math.max(0, Math.floor(seen.left / grid) * grid);
    const fromY = Math.max(0, Math.floor(seen.top / grid) * grid);
    const toX = Math.min(map.width, seen.right);
    const toY = Math.min(map.height, seen.bottom);

    for (let x = fromX; x <= toX; x += grid) {
      gridLayer.moveTo(x, fromY).lineTo(x, toY);
    }

    for (let y = fromY; y <= toY; y += grid) {
      gridLayer.moveTo(fromX, y).lineTo(toX, y);
    }

    // `alpha` rides along on the stroke rather than replacing the token's own: the
    // grid is decoration over artwork and must be quieter than a placement edge,
    // but "quieter" is a fraction of the same measured colour, not a second one.
    gridLayer.stroke({ ...palette["map-outline"], width: outlineEdge(), alpha: 0.55 });
  }

  function outlineEdge() {
    return camera.worldWidthForScreenPixels(cameraState, outlinePixels);
  }

  function redrawFog() {
    fogLayer.clear();

    if (!Array.isArray(state.fog)) {
      return;
    }

    const seen = camera.visibleWorld(cameraState, options.viewport);

    for (const cell of state.fog) {
      const tint = fadeTint(palette["map-dim"], fogSteps[cell.state]);

      if (tint == null || tint.alpha === 0) {
        continue;
      }

      // Clipped to what the camera shows and to the map, because a fog rectangle
      // for a room nobody is looking at is a rectangle re-issued on every pan.
      const left = Math.max(cell.left, seen.left, 0);
      const top = Math.max(cell.top, seen.top, 0);
      const right = Math.min(cell.right, seen.right, map.width);
      const bottom = Math.min(cell.bottom, seen.bottom, map.height);

      if (right <= left || bottom <= top) {
        continue;
      }

      fogLayer.rect(left, top, right-left, bottom-top).fill(tint);
    }
  }

  /** Builds the display objects for one placement, empty and unattached. */
  function buildView(placement) {
    const disc = new Graphics();
    const ring = new Graphics();
    const label = new Text({ text: placement.id, style: textStyle(0) });
    const view = new Container();

    label.anchor.set(0.5);
    view.addChild(disc, label, ring);
    view.disc = disc;
    view.ring = ring;
    view.label = label;

    return view;
  }

  /** Repaints one placement in place.
   *
   * Runs on every camera change as well as on every state change, because the
   * outline width and the label size are both screen-space constants and so have
   * to be re-derived when the scale moves. The view is reused, so a pan does not
   * churn the render groups.
   */
  function paint(view, placement) {
    const radius = grid / 2;
    const edge = outlineEdge();
    const body = hitPointTint(placement, palette);

    view.disc.clear();
    view.ring.clear();

    if (body != null) {
      view.disc.circle(0, 0, radius).fill(body);
    }

    view.disc.circle(0, 0, radius).stroke({ ...palette["map-outline"], width: edge });

    if (placement.id === selected && palette.accent != null) {
      view.ring.circle(0, 0, radius + edge * 2).stroke({ ...palette.accent, width: edge });
    }

    view.label.style = textStyle(camera.labelWorldSize(cameraState, labelPixels, palette.typeScale));
    view.label.y = radius + edge;
    view.position.set(placement.x, placement.y);
  }

  function redrawPlacements() {
    const seen = camera.visibleWorld(cameraState, options.viewport);
    const drawn = new Set();

    for (const placement of livePlacements(state)) {
      const box = {
        left: placement.x - grid,
        right: placement.x + grid,
        top: placement.y - grid,
        bottom: placement.y + grid
      };

      if (!camera.overlaps(box, seen)) {
        continue;
      }

      drawn.add(placement.id);

      let view = views.get(placement.id);

      if (view == null) {
        view = buildView(placement);
        views.set(placement.id, view);
        placements.addChild(view);
      }

      paint(view, placement);
    }

    // Anything that left the view, or left the state, stops costing a display
    // object and a text mesh.
    for (const [id, view] of [...views]) {
      if (drawn.has(id)) {
        continue;
      }

      view.destroy({ children: true });
      placements.removeChild(view);
      views.delete(id);
    }
  }

  function redrawStrip() {
    strip.clear();

    for (const label of stripLabels.removeChildren()) {
      label.destroy();
    }

    const order = Array.isArray(state.initiative) ? state.initiative : [];

    if (order.length === 0 || palette.text == null || palette["map-outline"] == null) {
      return;
    }

    const diameter = camera.screenSize(markerPixels, palette.typeScale);
    const slot = options.viewport.width / order.length;

    for (let index = 0; index < order.length; index += 1) {
      const body = order[index] === state.activePlacement ? palette.accent : palette.surface;
      const centre = (index + 0.5) * slot;
      const disc = strip.circle(centre, diameter, diameter / 2);

      if (body != null) {
        disc.fill(body);
      }

      // `outlinePixels` directly, not the map-unit conversion: the strip is drawn
      // in screen space, so its strokes are already screen pixels and dividing by
      // the camera's scale here would make the initiative ring the one edge on the
      // map that grows as you zoom out.
      disc.stroke({ ...palette["map-outline"], width: outlinePixels });

      const label = new Text({
        text: String(index + 1),
        style: textStyle(camera.screenSize(labelPixels, palette.typeScale))
      });

      label.anchor.set(0.5);
      label.position.set(centre, diameter);
      stripLabels.addChild(label);
    }
  }

  function textStyle(fontSize) {
    const style = { fontFamily: "system-ui, sans-serif", fontSize };

    if (palette.text != null) {
      style.fill = palette.text.color;
    }

    return style;
  }

  /** Repaints every layer from the camera and the state, and renders one frame. */
  function render() {
    if (destroyed) {
      return;
    }

    applyCamera();
    redrawGrid();
    redrawFog();
    redrawPlacements();
    redrawStrip();
    app.render();
  }

  // Paint once before returning, so the scene image is on screen before any state
  // arrives.
  //
  // Found by screenshotting a mounted map: the surface was blank until the first
  // `setState`, which on a slow socket is a blank map for as long as the connection
  // takes — and a reader cannot tell that from a map that failed to load. The state
  // may legitimately be empty; the artwork is not.
  render();

  return {
    canvas,

    // The PixiJS application, for a caller that has to look at the scene graph — the
    // committed end-to-end tests of C6 assert on it, and there is nothing else to
    // reach. Exposed read-only by convention: a caller that reaches in and mutates
    // the stage has left the layer order this file's contrast argument depends on.
    app,

    /** Replaces the whole state document and repaints. */
    setState(next) {
      state = next || {};
      render();
    },

    /** The viewport changed size or orientation: `resizeCamera`, never `fitCamera`. */
    resize(viewport) {
      options.viewport = viewport;
      app.renderer.resize(viewport.width, viewport.height);
      // The one place in this tree that answers a resize, and it is
      // `resizeCamera` because it must be. `fitCamera` would re-centre on the
      // scene's middle and re-scale to fit, which is the re-framing UI §7.6
      // forbids and which `TestTheMapSurvivesAResizeWithoutReframing` fails.
      cameraState = camera.resizeCamera(cameraState, viewport);
      render();
    },

    /** A pointer drag of `delta` screen pixels. */
    pan(delta) {
      cameraState = camera.panScreen(cameraState, delta);
      render();
    },

    /** A wheel or pinch zoom of `factor` about a screen point. */
    zoom(factor, focus) {
      cameraState = camera.zoomAt(
        cameraState,
        options.viewport,
        focus,
        factor,
        camera.fitCamera(map, options.viewport).worldPerViewport,
        nearestWorldPerViewport
      );
      render();
    },

    /** Centres on a placement, for the token list's "select and move the camera". */
    select(placementId) {
      selected = placementId;

      for (const placement of livePlacements(state)) {
        if (placement.id === placementId) {
          cameraState = camera.centreOn(cameraState, { x: placement.x, y: placement.y });
        }
      }

      render();
    },

    /** The placement under a screen point, or `null`. */
    placementAt(point) {
      const here = camera.screenToWorld(cameraState, options.viewport, point);
      let found = null;
      let nearest = grid;

      for (const placement of livePlacements(state)) {
        const distance = camera.mapDistance(here, { x: placement.x, y: placement.y });

        if (distance <= nearest) {
          nearest = distance;
          found = placement;
        }
      }

      return found;
    },

    /** The current camera, for a caller that wants to read it. */
    get camera() {
      return cameraState;
    },

    destroy() {
      if (destroyed) {
        return;
      }

      destroyed = true;
      views.clear();
      app.destroy({ removeView: true }, { children: true, texture: true, textureSource: true });
    }
  };
}

/** The placements of a state document, or none.
 *
 * `realtime.Document.Placements` is a slice that marshals to `null` when empty,
 * so `Array.isArray` is the check and not a truthiness test.
 */
function livePlacements(state) {
  return Array.isArray(state.placements) ? state.placements : [];
}

/** `value` when it is a positive finite number, and `fallback` when it is not. */
function positive(value, fallback) {
  return Number.isFinite(value) && value > 0 ? value : fallback;
}

/** The tint for a placement's hit points.
 *
 * Three bands over `maxHp`, using tokens that already mean this elsewhere in the
 * shell. `maxHp` of zero — a placement whose page has not been filled in — counts
 * as whole rather than dividing by zero.
 */
function hitPointTint(placement, palette) {
  if (!(placement.maxHp > 0)) {
    return palette.success;
  }

  const fraction = placement.hp / placement.maxHp;

  if (fraction <= 0.25) {
    return palette.danger;
  }

  if (fraction <= 0.6) {
    return palette.warning;
  }

  return palette.success;
}