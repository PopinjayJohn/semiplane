// palette.js — the only place a colour enters this client.
//
// # The rule
//
// **No colour literal anywhere else in `internal/web/static/js/map/`.** Every
// colour the canvas paints is read from a CSS custom property on the map surface,
// normalised through the browser, and handed to PixiJS as a `{ color, alpha }`
// pair. There is no `#rrggbb`, no `rgb(`, and no named colour anywhere in this
// tree, and `TestTheMapSourceHoldsTheContrastAndAccessibilityContract` fails the
// build if one appears — it reads the **shipped bytes through a lexer**, so a
// colour named inside a comment is prose rather than a violation, and a colour in
// a string literal is a violation.
//
// The reason is phase 5's §6.2 and §6.7. `--map-dim` is 18% black in light and 32%
// in dark and nothing promotes it; `--map-outline` is an alias of `--text-subtle`
// and follows it when `prefers-contrast: more` promotes that. A canvas carrying
// its own copy of either value would be a **second answer** to the same question:
// right on the day it was written, wrong the moment somebody turns on forced
// colours, and invisible to `tokens_contrast_test.go`, which measures the
// stylesheet rather than the script.
//
// # Why values are normalised rather than passed through
//
// PixiJS has its own colour grammar, and whether it accepts a given CSS syntax is a
// property of the engine rather than of the platform. So the value is not handed to
// PixiJS as written: it is painted into a throwaway 2D context and read back, because
// that is the one component in the stack whose job is to know every colour syntax,
// and it is already in the page.
//
// **The read-back is not hex, and that is the reason this file has a parser at all.**
// Measured in Chromium: a custom property holding `rgb(0 0 0 / 0.18)` comes back
// from `getPropertyValue` as `#0000002e`, and painting *that* into a 2D context and
// reading it back gives `rgba(0, 0, 0, 0.18)` — the canvas serialisation of a
// non-opaque colour, not the hex it accepts. An earlier version of this file matched
// only `#rrggbb`/`#rrggbbaa`, so `--map-dim` — the one token UI §7.6 names first —
// resolved to `null` in every browser and `createScene` refused to start. The Go gate
// could not see it: there is no Node in CI, and a browser's colour serialisation is
// not a thing a text audit can assert.
//
// The result is split into `{ color, alpha }` rather than left as one string,
// because Pixi's `fill` and `stroke` take alpha as its own field. Keeping alpha out
// of the colour means the fog ladder in `scene.js` can scale it as a number, and it
// means a translucent token never becomes an opaque one by being truncated.
//
// # The fallback, and why it is a refusal
//
// An unreadable token yields `null`, and this file does not substitute a default
// colour — `scene.js` refuses to build without `--map-dim` and `--map-outline`. A
// canvas that silently painted a substitute would be showing a state the
// accessibility gate has never measured, which is exactly the failure
// `TestTheBuiltStylesheetCarriesTheTokensAndTheGrid` catches on the CSS side.
// Reading `--map-dim` out of a document that never declared it is a deployment
// fault and it should look like one.

/** The colour tokens this client reads.
 *
 * A closed list rather than a wildcard, because a wildcard would make the Go audit
 * unable to say *which* tokens the canvas depends on — and that list is the inventory
 * a reviewer needs when a theme layer (C5) has to override one of them.
 *
 * Separate from `mapScaleToken` because `--type-scale` is a number and reading it
 * through the colour path would put a `null` in the record under a colour's name. A
 * record with a field that is always `null` is a field somebody will eventually read.
 */
export const mapTintTokens = [
  "--map-dim",
  "--map-outline",
  "--surface",
  "--accent",
  "--success",
  "--warning",
  "--danger",
  "--text",
];

/** The type scale, which §5.1 declares per tier and §7.6 requires in-canvas labels
 * to follow. Read on its own because it is a multiplier, not a colour.
 */
export const mapScaleToken = "--type-scale";

/** Reads the design tokens the map canvas paints with.
 *
 * `surface` is the **map surface element itself**, not the document element, and
 * that choice is load-bearing rather than incidental. Custom properties inherit,
 * so reading the surface resolves the nearest ancestor that declared each token —
 * which is how phase 9's C5 theme layer overrides `--map-dim` and
 * `--map-outline` for one campaign without this file knowing that themes exist.
 * Reading `documentElement` would pin the client to `:root`'s values and silently
 * ignore the override, which is the bug this sentence exists to prevent.
 *
 * Returns a record of the token names **without** their leading dashes, each
 * mapped to a `{ color, alpha }` tint, plus `typeScale` as a positive number.
 */
export function readPalette(surface, probe) {
  const style = surface.ownerDocument.defaultView.getComputedStyle(surface);
  const palette = {};

  for (const name of mapTintTokens) {
    palette[name.slice(2)] = readTint(style.getPropertyValue(name), probe);
  }

  const scale = Number.parseFloat(style.getPropertyValue(mapScaleToken));

  // `--type-scale` is a unitless multiplier (§5.1: 1.0625 at compact, 1.75 at TV).
  // A document whose shell stylesheet has not applied yet resolves it as the empty
  // string, and a `NaN` reaching a font size paints nothing at all — so the floor
  // is 1, which is the value the scale multiplies.
  palette.typeScale = Number.isFinite(scale) && scale > 0 ? scale : 1;

  return palette;
}

/** Canonicalises any CSS colour value into a PixiJS `{ color, alpha }` pair.
 *
 * `probe` is a 2D canvas context, created once per page: `fillStyle` accepts every
 * colour syntax the browser accepts, so this is a browser call rather than a colour
 * parser.
 *
 * Returns `null` for an empty value and for one the browser refuses. Refusal is
 * detected by the assignment leaving the sentinel in place, which is the specified
 * behaviour for an invalid `fillStyle` and the reason the sentinel is assigned first
 * rather than the field being read directly.
 */
export function readTint(value, probe) {
  const trimmed = String(value == null ? "" : value).trim();

  if (trimmed === "") {
    return null;
  }

  probe.fillStyle = probeSentinel;
  probe.fillStyle = trimmed;

  const canonical = probe.fillStyle;

  return canonical === probeSentinel ? null : parseColour(canonical);
}

/** `#rrggbb`, `#rrggbbaa`, and the `rgb()`/`rgba()` forms a canvas reads back.
 *
 * Both shapes are here because both are what a browser actually produces: hex for an
 * opaque colour and `rgba(…)` for a translucent one. A parser that knew only hex
 * would resolve `--map-dim` to `null`, which is the bug this shape list exists to
 * prevent.
 */
const hexColour = /^#([0-9a-f]{6})([0-9a-f]{2})?$/i;
const rgbColour =
  /^rgba?\(\s*(\d{1,3})\s*,\s*(\d{1,3})\s*,\s*(\d{1,3})\s*(?:,\s*([0-9.]+)\s*(%?)\s*)?\)$/i;

/** Parses what the canvas handed back, or `null` for a shape it does not have.
 *
 * Two shapes and no more: hex, and the legacy-comma `rgb()`/`rgba()` form the canvas
 * API serialises to. A colour this cannot read is a colour this refuses, rather than
 * one it approximates — an unparsed token means `createScene` stops, which is a
 * visible failure, and an approximately-parsed one means the canvas paints something
 * `tokens_contrast_test.go` has never measured.
 */
function parseColour(canonical) {
  const hex = hexColour.exec(canonical);

  if (hex != null) {
    return {
      color: `#${hex[1].toLowerCase()}`,
      alpha: hex[2] == null ? 1 : Number.parseInt(hex[2], 16) / 255,
    };
  }

  const rgb = rgbColour.exec(canonical);

  if (rgb == null) {
    return null;
  }

  const [, red, green, blue, alpha, percent] = rgb;

  if (Number(red) > 255 || Number(green) > 255 || Number(blue) > 255) {
    return null;
  }

  return {
    color: `#${[red, green, blue]
      .map((part) => Number(part).toString(16).padStart(2, "0"))
      .join("")}`,
    alpha: alphaFraction(alpha, percent),
  };
}

/** The alpha of a serialised colour, from a fraction or from a percentage.
 *
 * The canvas API serialises alpha as a fraction today and as a percentage in some
 * engines' `toString`, so both are read and an absent one is opaque. A value outside
 * `[0, 1]` is `null`: a negative or greater-than-one alpha is a browser that
 * serialised something this file does not understand, and guessing is worse than
 * refusing.
 */
function alphaFraction(alpha, percent) {
  if (alpha == null) {
    return 1;
  }

  const parsed = Number.parseFloat(alpha) * (percent === "%" ? 100 : 1);

  return parsed >= 0 && parsed <= 1 ? parsed : null;
}

/** The sentinel that tells "the browser refused this value" from "the value is
 * black".
 *
 * A named constant because it appears in two places — the assignment and the
 * comparison — and a string literal in both is two chances to write one of them
 * differently, at which point every unreadable colour silently reads as opaque black.
 */
const probeSentinel = "#000000";

/** The same tint at a fraction of its own alpha.
 *
 * Fog of war is a ladder — unexplored, remembered, visible — and the ladder must
 * not be a hardcoded `0.4`. Deriving each step from `--map-dim`'s own alpha is
 * what keeps the ladder following the theme: double `--map-dim` for darkness and
 * every step follows it, with no second number to keep in step.
 *
 * Returns `null` for a `null` tint and for a fraction outside `[0, 1]`, so a
 * caller cannot turn "unknown" into "fully opaque black".
 */
export function fadeTint(tint, fraction) {
  if (tint == null || !(fraction >= 0) || fraction > 1) {
    return null;
  }

  return { color: tint.color, alpha: tint.alpha * fraction };
}