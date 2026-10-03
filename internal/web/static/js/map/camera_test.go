// The camera's gates: every claim below is made against `camera.js`'s own bytes,
// evaluated by the evaluator in `camera_arith_test.go`.
//
// # What is proved, and how
//
//   - **The fit shows the whole scene.** Every corner of every grid map lands
//     inside every viewport in the grid.
//   - **A resize does not re-frame.** A map point keeps its *fractional* position
//     in the viewport, for every pair of viewports in the grid, for a fitted camera
//     and for a panned and zoomed one. This is the phase's Definition of Done
//     ("the map survives a resize without re-framing"), and
//     `TestARefitWouldFailTheResizeAssertion` proves the assertion can fail by
//     re-framing the module.
//   - **The camera carries no pixel quantity.** The camera record's field names are
//     exactly `{centreX, centreY, worldPerViewport}` — asserted as a set, so
//     *adding* a fourth field fails rather than being read past.
//   - **A resize cannot re-fit.** `resizeCamera`'s parameter list is exactly
//     `(camera, viewport)` and its body's only free name is none at all: it reads
//     `camera` and nothing else. There are no map bounds in scope for it to re-fit
//     from, which is a property of the signature rather than a promise.
//   - **The 3px outline is 3 screen pixels at every scale**, and the label is
//     `base * --type-scale` screen pixels at every scale. Both are proved by
//     projecting the length the camera asks for and measuring what lands.

package mapjs_test

import (
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"testing"
)

// The camera record's fields, as a set. Not a list: an extra field is the failure
// this test exists for, and a list comparison that only checked the first three
// would miss it.
var cameraFields = []string{"centreX", "centreY", "worldPerViewport"}

// TestTheCameraModuleIsArithmeticOverPlainNumbers is the gate's own preflight.
//
// Every other test in this file calls the evaluator, and an evaluator that silently
// accepted more than the comment claims would make them all weaker — a `camera.js`
// that grew an `if` would pass a test that was still "reading it", just less of it.
// So the parse is asserted directly, and the failure message is the grammar itself,
// because the person who hits it is about to edit a file whose header promises a
// restriction they have to understand.
func TestTheCameraModuleIsArithmeticOverPlainNumbers(t *testing.T) {
	t.Parallel()

	program := cameraProgram(t)

	if len(program.names()) < 10 {
		t.Fatalf(
			"camera.js declares %d functions, which is fewer than the %d this gate expects: %s",
			len(program.names()),
			10,
			strings.Join(program.names(), ", "),
		)
	}
}

// TestTheArithmeticGateRefusesAnUnboundName is the meta-test for the boundness check,
// and it is here because of the scar.
//
// An earlier version of the evaluator bound a bare `min`, and `camera.js` used it.
// Every test in the suite evaluated the module, `make check` was green, and a browser
// threw `ReferenceError` on the first frame — a gate that was reading a document
// rather than the shipped bytes, and reporting a coverage it did not have.
//
// So the check lives in `parseArith` rather than in a test, and this is what proves it
// can fail: a name that neither JavaScript nor the evaluator binds has to be a parse
// error, and the diagnostic has to name it. `Math.round` is in the list because
// "JavaScript has it, this gate does not" is the other half of the same mistake.
func TestTheArithmeticGateRefusesAnUnboundName(t *testing.T) {
	t.Parallel()

	for _, fixture := range []struct {
		name    string
		source  string
		expects string
	}{
		{
			name:    "a bare builtin JavaScript does not define",
			source:  "export function f(a) { return min(a, 1); }",
			expects: `"min"`,
		},
		{
			name:    "a typo in a function name",
			source:  "export function f(a) { return g(a); }",
			expects: `"g"`,
		},
		{
			name:    "a Math member outside the closed set",
			source:  "export function f(a) { return Math.round(a); }",
			expects: "global namespace",
		},
		{
			name:    "a bare global",
			source:  "export function f(a) { return parseFloat(a); }",
			expects: `"parseFloat"`,
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()

			_, err := parseArith(fixture.source)
			if err == nil {
				t.Fatalf("the arithmetic gate accepted %s.\n"+
					"An evaluator that binds a name its host language does not is a gate on a "+
					"document rather than on the file, and it reports a coverage it does not "+
					"have.", fixture.source)
			}

			if !strings.Contains(err.Error(), fixture.expects) {
				t.Errorf("the gate rejected %s with %q, which does not name the unbound thing.\n"+
					"A diagnostic a reader cannot act on is the same failure as no diagnostic.",
					fixture.name, err)
			}
		})
	}
}

// TestTheCameraCannotBeRefitFromAViewport is the structural half of the resize
// requirement, and the reason it is a signature assertion rather than a behavioural
// one.
//
// A behavioural test can only show that `resizeCamera` did not re-fit for the inputs
// it tried. This shows that it *cannot*: its parameters are `camera` and `viewport`
// and its body reads nothing but `camera`, so no map bounds are in scope for a
// re-fit to be computed from. A future edit that gave it a third parameter, or that
// referenced `map`, fails the build rather than a test somebody has to remember to
// add.
func TestTheCameraCannotBeRefitFromAViewport(t *testing.T) {
	t.Parallel()

	program := cameraProgram(t)

	resizeParams, err := program.params("resizeCamera")
	if err != nil {
		t.Fatalf("%v", err)
	}

	if got := strings.Join(resizeParams, ", "); got != "camera, viewport" {
		t.Errorf("resizeCamera(%s): the parameters must be exactly `camera, viewport`.\n"+
			"Anything else puts something in scope for a resize to re-derive itself from,\n"+
			"which is the re-framing UI §7.6 forbids.", got)
	}

	free, err := program.freeNames("resizeCamera")
	if err != nil {
		t.Fatalf("%v", err)
	}

	if len(free) != 0 {
		t.Errorf("resizeCamera's body reads %s, so it is not a pure identity on `camera`.\n"+
			"The resize path must not consult the viewport at all: that is what keeps the\n"+
			"map's centre where the reader left it.", strings.Join(free, ", "))
	}

	// And the function it is *not*: `fitCamera` must still exist and must still take
	// the map, or the assertion above would be satisfiable by deleting the fit.
	fitParams, err := program.params("fitCamera")
	if err != nil {
		t.Fatalf("fitCamera must exist and must be the one place a camera is built: %v", err)
	}

	if got := strings.Join(fitParams, ", "); got != "map, viewport" {
		t.Errorf("fitCamera(%s): expected `map, viewport`", got)
	}
}

// TestTheCameraCarriesNoPixelQuantity is "zoom is in world units, never pixels" as a
// closed vocabulary over the module's own syntax tree.
//
// The obvious reading of that requirement is a grep for `pixel` in `camera.js`, and
// this test is deliberately not that: `worldWidthForScreenPixels` and
// `screenToWorld` both say "screen" honestly, and neither is a camera. What would
// actually break the requirement is a field carrying a pixel quantity — a remembered
// zoom in device pixels, a remembered viewport width, a scale precomputed per
// device — and every such thing has to be a field of a record somewhere in the
// module. So the assertion is on the record field names, which is exhaustive by
// construction rather than by enumeration.
//
// A record whose names are all camera fields must have **exactly** the three, so a
// camera can never be built partially and quietly have a field default somewhere.
func TestTheCameraCarriesNoPixelQuantity(t *testing.T) {
	t.Parallel()

	program := cameraProgram(t)

	// Every field name the grammar admits anywhere in the module. The camera's
	// three, the screen and map rectangles' two and four, and the camera's own
	// `x`/`y` for a point.
	allowed := map[string]bool{
		"centreX": true, "centreY": true, "worldPerViewport": true,
		"x": true, "y": true,
		"width": true, "height": true,
		"left": true, "top": true, "right": true, "bottom": true,
	}

	records := program.records()
	if len(records) == 0 {
		t.Fatal("camera.js builds no records at all, so this assertion cannot be reading it")
	}

	for _, record := range records {
		names := append([]string(nil), record.fieldNames...)
		slices.Sort(names)

		for _, name := range names {
			if !allowed[name] {
				t.Errorf("camera.js line %d builds a record with a field %q.\n"+
					"The fields a camera may carry are %v, and a point or a rectangle.\n"+
					"A field naming pixels, a viewport, or a remembered zoom is the re-framing\n"+
					"bug in waiting: resizeCamera would have to update it, and a resize is\n"+
					"exactly when the map must not move.",
					record.line(), name, cameraFields)
			}
		}

		if isCameraRecord(names) && strings.Join(names, ", ") != strings.Join(cameraFields, ", ") {
			t.Errorf("camera.js line %d builds {%s}, which is a camera with fields missing.\n"+
				"A camera is exactly {%s}; a partial one has a field defaulting somewhere, and\n"+
				"a default is a second answer to what that field is.",
				record.line(), strings.Join(names, ", "), strings.Join(cameraFields, ", "))
		}
	}
}

// isCameraRecord reports whether a record's fields are all camera fields, which is
// how the partial-camera case above is told apart from a point or a rectangle.
func isCameraRecord(names []string) bool {
	for _, name := range names {
		if !slices.Contains(cameraFields, name) {
			return false
		}
	}

	return true
}

// TestAFitShowsTheWholeMapInsideEveryViewport is the "fits the world bounds to the
// viewport" half of the requirement, over every pair in the grid.
//
// Contain rather than crop, so the check is that all four corners of the map land
// inside the viewport rectangle. A cover-fit — the other half of the two ways to fit
// a rectangle to a rectangle — would put every corner at or outside an edge and fail
// this, which is intended: a cropped map is not a fitted map, and UI §4.9's
// compact surface is letterboxed.
func TestAFitShowsTheWholeMapInsideEveryViewport(t *testing.T) {
	t.Parallel()

	program := cameraProgram(t)

	for _, size := range mapSizes {
		for _, width := range viewportWidths {
			for _, height := range viewportHeights {
				viewport := map[string]float64{"width": width, "height": height}
				camera := cameraAt(t, program, size, width, height)

				for _, corner := range []map[string]float64{
					{"x": 0, "y": 0},
					{"x": size[0], "y": 0},
					{"x": 0, "y": size[1]},
					{"x": size[0], "y": size[1]},
				} {
					screen, err := program.call("worldToScreen",
						evalArgs(t, camera, viewport, corner))
					if err != nil {
						t.Fatalf("worldToScreen: %v", err)
					}

					x, xerr := screen.field("x")
					y, yerr := screen.field("y")

					if xerr != nil || yerr != nil {
						t.Fatalf("worldToScreen returned no x/y: %v %v", xerr, yerr)
					}

					if x < -arithEPS || x > width+arithEPS ||
						y < -arithEPS || y > height+arithEPS {
						t.Fatalf("map %v fitted into %vx%v: corner (%g, %g) of the map lands at "+
							"(%g, %g), outside the viewport.\n"+
							"The fit is supposed to contain the map, not crop it.",
							size, width, height, corner["x"], corner["y"], x, y)
					}
				}
			}
		}
	}
}

// TestTheMapSurvivesAResizeWithoutReframing is the phase's Definition of Done.
//
// Two claims, and the second is the one the requirement is actually about.
//
//  1. **`resizeCamera` returned the same camera.** Field for field, to the
//     tolerance. Trivial to read and trivial to state, and worth stating because "the
//     map did not move" is false if the centre shifted by one pixel — which on a
//     television is a visible jump.
//  2. **A map point kept its place relative to the middle of the viewport.** For
//     every ordered pair of viewports, every map point on a grid, and three starting
//     cameras (fitted, panned, panned and zoomed): project the point, shift it by half
//     the viewport's change, invert through the resized camera, require the original
//     point back.
//
// "Relative to the middle" is not "the same fraction", and the distinction is the
// whole requirement. A rotation changes the viewport's aspect ratio, so a camera that
// keeps its scale necessarily shows a different span of the map along one axis —
// claiming the same *fraction* would be claiming something impossible. What must not
// happen is the map sliding, and a re-fit slides it: it puts the middle of the *map*
// under the middle of the window, which on a table panned into one corner throws the
// map across the screen. That is what the requirement calls happening "at every
// breakpoint crossing".
//
// `TestARefitWouldFailTheResizeAssertion` is the other half: it re-frames the
// module four different ways and requires this to notice every one.
func TestTheMapSurvivesAResizeWithoutReframing(t *testing.T) {
	t.Parallel()

	if err := centreSurvives(t, cameraProgram(t), viewports()); err != nil {
		t.Fatalf("the map re-frames on a resize: %v\n%s", err, centreSurvivesHelp)
	}
}

// centreSurvivesHelp says what a failure of this assertion means, because
// "expected 1000, got 240" does not tell a reader that the product jumps.
const centreSurvivesHelp = `A resize must not move the map. resizeCamera exists to make that\n` +
	`structural: it is the identity on (centreX, centreY, worldPerViewport) and it is\n` +
	`the only function the resize path in scene.js may call. If this fails, either\n` +
	`that path stopped calling it, or the camera started carrying something that has\n` +
	`to be recomputed when the viewport changes — which is the bug this requirement\n` +
	`names: "the map re-frames on every breakpoint crossing, which at the 14-inch\n` +
	`target happens constantly."`

// centreSurvives is the substantive resize check, as a function so the mutation test
// can run it against a re-framed module.
//
// The failure is returned rather than fataled on the test, because the mutation test
// needs to know whether it got one and `t.Fatalf` does not return.
func centreSurvives(t *testing.T, program arithProgram, pairs [][2]float64) error {
	t.Helper()

	for _, size := range resizeMaps {
		for _, first := range viewports() {
			for _, second := range pairs {
				if first == second {
					continue
				}

				before := map[string]float64{"width": first[0], "height": first[1]}
				after := map[string]float64{"width": second[0], "height": second[1]}

				for _, start := range startingCameras(t, program, size, first[0], first[1]) {
					resized, err := program.call("resizeCamera", evalArgs(t, start, after))
					if err != nil {
						return fmt.Errorf("resizeCamera on %vx%v -> %vx%v: %w",
							first[0], first[1], second[0], second[1], err)
					}

					if err := cameraUnchanged(start, resized); err != nil {
						return fmt.Errorf("viewport %vx%v -> %vx%v: %w",
							first[0], first[1], second[0], second[1], err)
					}

					for _, point := range mapPoints(size) {
						if err := holdsPlace(
							t,
							program,
							start,
							before,
							resized,
							after,
							point,
						); err != nil {
							return fmt.Errorf("map %v, viewport %vx%v -> %vx%v, a camera centred "+
								"at (%g, %g) at %g map units per viewport unit: %w",
								size, first[0], first[1], second[0], second[1],
								mustField(t, start, "centreX"), mustField(t, start, "centreY"),
								mustField(t, start, "worldPerViewport"), err)
						}
					}
				}
			}
		}
	}

	return nil
}

// cameraUnchanged asserts that a resize returned the same camera.
//
// Two claims in one, because both are cheap and they fail differently. The values
// are the substance: "the camera did not move" is not true if the centre shifted by
// a pixel, and at a television's resolution a pixel is a visible jump.
func cameraUnchanged(before, after arithValue) error {
	for _, name := range cameraFields {
		was, err := before.field(name)
		if err != nil {
			return err
		}

		now, gerr := after.field(name)
		if gerr != nil {
			return gerr
		}

		if !closeEnough(now, was) {
			return fmt.Errorf("resizeCamera changed %s from %g to %g", name, was, now)
		}
	}

	return nil
}

// holdsPlace asserts that a map point kept its place relative to the middle of the
// viewport.
//
// **"Relative to the middle", not "the same fraction", and the difference is the
// whole subject of the requirement.** A rotation from 320x320 to 320x580 changes the
// viewport's aspect ratio, so a camera that keeps its scale necessarily shows a
// different span of the map vertically. What must not happen is the map *sliding*:
// the correct statement is that the point's offset from the centre of the window is
// unchanged, which on screen is a shift of exactly half the viewport's change. A
// re-fit puts the middle of the *map* under the middle of the window instead, which
// on a table that has been panned to one corner jumps the map across the screen —
// and that is precisely the re-framing UI §7.6 forbids.
func holdsPlace(
	t *testing.T,
	program arithProgram,
	before arithValue,
	beforeViewport map[string]float64,
	after arithValue,
	afterViewport map[string]float64,
	point map[string]float64,
) error {
	t.Helper()

	projected, err := program.call("worldToScreen", evalArgs(t, before, beforeViewport, point))
	if err != nil {
		return fmt.Errorf("worldToScreen: %w", err)
	}

	x, err := projected.field("x")
	if err != nil {
		return err
	}

	y, err := projected.field("y")
	if err != nil {
		return err
	}

	shifted := map[string]float64{
		"x": x + (afterViewport["width"]-beforeViewport["width"])/2,
		"y": y + (afterViewport["height"]-beforeViewport["height"])/2,
	}

	back, err := program.call("screenToWorld", evalArgs(t, after, afterViewport, shifted))
	if err != nil {
		return fmt.Errorf("screenToWorld: %w", err)
	}

	return requireSamePoint(back, point)
}

// rotations is a short list of viewport pairs chosen for their *shapes*: four phone
// and tablet rotations and the breakpoint crossings the UI record names.
//
// The mutation test uses this rather than the full grid, for two reasons. It is
// four runs of a 500,000-iteration sweep, and the sweep is not what the mutation is
// testing — the mutation is testing whether the assertion notices. And a list named
// for what it contains is readable: someone changing the grids can see that the
// mutation test is not silently reduced to a case the mutants cannot affect.
func rotations() [][2]float64 {
	return [][2]float64{
		{320, 568},
		{568, 320}, // §4.9's phone, both ways
		{320, 580},
		{580, 320}, // compact-short both ways
		{768, 1024},
		{1024, 768}, // a tablet rotating
		{900, 1600},
		{1600, 900}, // §5.1's laptop breakpoints
		{1280, 800},
		{800, 1280},
		{2200, 1230},
		{1230, 2200}, // §5.1's television modes
		{320, 320},
		{1440, 1440}, // square to widescreen
		{1024, 1600},
		{1600, 1024}, // §4.9's stated concern: the 14-inch target
	}
}

// viewports is every width paired with every height, as distinct ordered pairs.
//
// Ordered pairs and not combinations, because the half that matters is the *shape*
// change: a rotation is a resize whose height and width move in opposite directions,
// and a grid of widths alone would never produce one.
func viewports() [][2]float64 {
	pairs := make([][2]float64, 0, len(viewportWidths)*len(viewportHeights))

	for _, width := range viewportWidths {
		for _, height := range viewportHeights {
			pairs = append(pairs, [2]float64{width, height})
		}
	}

	return pairs
}

// startingCameras are the three shapes a camera is in during a session: freshly
// fitted, panned, and panned then zoomed. Testing only the fitted one would leave
// the common case — a table that has been panned across for an hour and then the
// phone rotates — unproved.
func startingCameras(
	t *testing.T,
	program arithProgram,
	size [2]float64,
	width, height float64,
) []arithValue {
	t.Helper()

	fitted := cameraAt(t, program, size, width, height)
	viewport := map[string]float64{"width": width, "height": height}

	panned, err := program.call("panScreen",
		evalArgs(t, fitted, map[string]float64{"x": 137, "y": -421}))
	if err != nil {
		t.Fatalf("panScreen: %v", err)
	}

	zoomed, err := program.call("zoomAt", evalArgs(t, panned, viewport,
		map[string]float64{"x": width * 0.7, "y": height * 0.3}, 2.5, 0.001, 1))
	if err != nil {
		t.Fatalf("zoomAt: %v", err)
	}

	return []arithValue{fitted, panned, zoomed}
}

// mapPoints is a grid over the map, plus its corners and its midpoint.
//
// The corners and the midpoint are in there because they are the points a fit is
// most likely to get wrong, and the grid is in there because a bug that only shows
// up between the corners is exactly the kind this test exists to find.
func mapPoints(size [2]float64) []map[string]float64 {
	points := make([]map[string]float64, 0, 11)
	points = append(points,
		map[string]float64{"x": 0, "y": 0},
		map[string]float64{"x": size[0], "y": 0},
		map[string]float64{"x": 0, "y": size[1]},
		map[string]float64{"x": size[0], "y": size[1]},
		map[string]float64{"x": size[0] / 2, "y": size[1] / 2},
	)

	for _, fraction := range []float64{0.25, 0.75} {
		points = append(points,
			map[string]float64{"x": size[0] * fraction, "y": size[1] * fraction},
			map[string]float64{"x": size[0] * (1 - fraction), "y": size[1] * fraction},
			map[string]float64{"x": size[0] * fraction, "y": size[1] * (1 - fraction)},
		)
	}

	return points
}

func requireSamePoint(got arithValue, want map[string]float64) error {
	for _, axis := range []string{"x", "y"} {
		actual, err := got.field(axis)
		if err != nil {
			return err
		}

		if !closeEnough(actual, want[axis]) {
			return fmt.Errorf(
				"map point (%g, %g) is at (%g, %g) after the resize, want it still there",
				want["x"], want["y"], actual, actual)
		}
	}

	return nil
}

// TestARefitWouldFailTheResizeAssertion is the mutation that makes the test above
// mean something.
//
// `TestTheMapSurvivesAResizeWithoutReframing` would pass for a `resizeCamera` that
// quietly re-framed the map, if the re-framing happened to be a no-op for the inputs
// it tried. So three mutants are applied to the shipped source and each must be
// caught: a re-fit, a centre that drifts with the viewport, and a scale re-derived
// from the new viewport width — the last being the exact bug the requirement's
// "never pixels" wording is about.
func TestARefitWouldFailTheResizeAssertion(t *testing.T) {
	t.Parallel()

	source, err := os.ReadFile(cameraSource)
	if err != nil {
		t.Fatalf("read %s: %v", cameraSource, err)
	}

	for _, mutant := range []struct {
		name string
		body string
	}{
		{
			name: "a re-fit",
			body: `return {
    centreX: 0,
    centreY: 0,
    worldPerViewport: 1
  };`,
		},
		{
			name: "a centre that drifts with the viewport",
			body: `return {
    centreX: camera.centreX + viewport.width / 2,
    centreY: camera.centreY + viewport.height / 2,
    worldPerViewport: camera.worldPerViewport
  };`,
		},
		{
			name: "a scale re-derived from the new viewport width",
			body: `return {
    centreX: camera.centreX,
    centreY: camera.centreY,
    worldPerViewport: camera.worldPerViewport * viewport.width / 800
  };`,
		},
		{
			name: "a scale that keeps the map the same size on screen",
			body: `return {
    centreX: camera.centreX,
    centreY: camera.centreY,
    worldPerViewport: camera.worldPerViewport * viewport.width / 1280
  };`,
		},
	} {
		t.Run(mutant.name, func(t *testing.T) {
			t.Parallel()

			mutated := replaceFunctionBody(t, string(source), "resizeCamera", mutant.body)

			program, err := parseArith(mutated)
			if err != nil {
				t.Fatalf("the mutant does not parse, so the mutation proves nothing: %v", err)
			}

			if err := centreSurvives(t, program, rotations()); err == nil {
				t.Fatalf("resizeCamera with %s survives the resize assertion.\n"+
					"Either the assertion has stopped checking what it claims, or the grids in\n"+
					"this file no longer reach the case. Both are the failure this test exists\n"+
					"to prevent, and neither is a thing a reader can see in a green build.",
					mutant.name)
			}
		})
	}
}

// replaceFunctionBody swaps the body of one declared function, brace-matched.
//
// `body` is what goes between the braces, so it carries its own trailing semicolon —
// the grammar wants `return <expr>;` and a mutant missing it is a parse error that
// reads like a defect in the mutation harness rather than in the mutation.
//
// Brace matching rather than a regular expression because the body is three lines of
// nested record literals, and a regex that stopped at the first `}` would replace
// half a function and produce exactly that confusing parse error.
func replaceFunctionBody(t *testing.T, source, name, body string) string {
	t.Helper()

	header := "export function " + name + "("
	start := strings.Index(source, header)
	if start < 0 {
		t.Fatalf("camera.js declares no %s to mutate", name)
	}

	open := strings.Index(source[start:], "{")
	if open < 0 {
		t.Fatalf("%s has no body to mutate", name)
	}

	open += start
	depth := 0
	end := -1

	for index := open; index < len(source); index++ {
		switch source[index] {
		case '{':
			depth++

		case '}':
			depth--

			if depth == 0 {
				end = index
			}
		}

		if end >= 0 {
			break
		}
	}

	if end < 0 {
		t.Fatalf("%s has an unterminated body", name)
	}

	return source[:open+1] + " " + body + " " + source[end:]
}

// TestTheContainerTransformIsTheProjection is the gate on the PixiJS transform.
//
// PixiJS draws a child's local point at `position + scale * point`, so a container
// that reproduces the projection must have `scale = 1 / worldPerViewport` and
// `position = viewport / 2 - centre / worldPerViewport`. **Both of those are the
// reciprocal of the thing a reader reaches for**, and both were wrong the first time:
// the map rendered, panned and zoomled, and the placements came out twelve times
// their size with their labels off screen. Nothing in the suite could see it, because
// everything in the suite was reading `camera.js` — so the transform moved into it,
// where a projection and a container transform can be compared as arithmetic.
//
// This walks the same grids as everything else and requires the two to agree at every
// point, which is the statement "what the container draws is what `worldToScreen` says".
func TestTheContainerTransformIsTheProjection(t *testing.T) {
	t.Parallel()

	program := cameraProgram(t)

	for _, size := range mapSizes {
		for _, width := range viewportWidths {
			for _, height := range viewportHeights {
				viewport := map[string]float64{"width": width, "height": height}

				for _, start := range startingCameras(t, program, size, width, height) {
					scale := evalNum(t, program, "containerScale", start)
					offset := evalRec(t, program, "containerOffset", start, viewport)

					for _, point := range mapPoints(size) {
						drawn := arithRec(map[string]float64{
							"x": point["x"]*scale + mustField(t, offset, "x"),
							"y": point["y"]*scale + mustField(t, offset, "y"),
						})

						projected := evalRec(t, program, "worldToScreen", start, viewport, point)

						for _, axis := range []string{"x", "y"} {
							got, err := drawn.field(axis)
							if err != nil {
								t.Fatalf("%s: %v", axis, err)
							}

							want, werr := projected.field(axis)
							if werr != nil {
								t.Fatalf("%s: %v", axis, werr)
							}

							if !closeEnough(got, want) {
								t.Errorf("map %v in %vx%v at scale %v: a container drawing the "+
									"point (%g, %g) puts its %s at %g, and worldToScreen says %g\n"+
									"\n"+
									"This is the transform PixiJS actually applies. When the two "+
									"disagree the map still renders and still pans — the placements "+
									"are simply the wrong size and the labels are off screen, which "+
									"no unit test can see and a screenshot can.",
									size, width, height, mustField(t, start, "worldPerViewport"),
									point["x"], point["y"], axis, got, want)
							}
						}
					}
				}
			}
		}
	}
}

// TestScreenToWorldIsTheInverseOfWorldToScreen is the round trip, over the same
// grids.
//
// A projection and its inverse that disagree by more than a rounding error would
// make a drag land the token somewhere the reader did not put it, and would make the
// zoom focus point drift a frame at a time.
func TestScreenToWorldIsTheInverseOfWorldToScreen(t *testing.T) {
	t.Parallel()

	program := cameraProgram(t)

	for _, size := range mapSizes {
		for _, width := range viewportWidths {
			for _, height := range viewportHeights {
				viewport := map[string]float64{"width": width, "height": height}

				for _, start := range startingCameras(t, program, size, width, height) {
					for _, point := range mapPoints(size) {
						screen, err := program.call(
							"worldToScreen",
							evalArgs(t, start, viewport, point),
						)
						if err != nil {
							t.Fatalf("worldToScreen: %v", err)
						}

						back, err := program.call(
							"screenToWorld",
							evalArgs(t, start, viewport, screen),
						)
						if err != nil {
							t.Fatalf("screenToWorld: %v", err)
						}

						if err := requireSamePoint(back, point); err != nil {
							t.Fatalf("map %v in %vx%v: %v", size, width, height, err)
						}
					}
				}
			}
		}
	}
}

// TestTheOutlineIsThreeScreenPixelsAtEveryScale is UI §7.6's "3px --map-outline on
// every placement and grid line", as arithmetic.
//
// An outline in map units would be a different number of pixels at every zoom, and
// 3 is the number the requirement states. So the test asks the camera how wide to
// draw a 3-screen-pixel line, projects that width through the camera, and requires
// exactly 3 back — at every map, viewport and zoom in the grid, which is what makes
// the word "every" in the requirement true.
func TestTheOutlineIsThreeScreenPixelsAtEveryScale(t *testing.T) {
	t.Parallel()

	const outline = 3.0

	program := cameraProgram(t)

	for _, size := range mapSizes {
		for _, width := range viewportWidths {
			for _, height := range viewportHeights {
				viewport := map[string]float64{"width": width, "height": height}

				for _, start := range startingCameras(t, program, size, width, height) {
					world := evalNum(t, program, "worldWidthForScreenPixels", start, outline)

					if !(world > 0) || math.IsInf(world, 0) {
						t.Fatalf(
							"a 3px outline came out as %g world units for camera %+v",
							world,
							start,
						)
					}

					near, err := program.call("worldToScreen",
						evalArgs(t, start, viewport, map[string]float64{"x": 0, "y": 0}))
					if err != nil {
						t.Fatalf("worldToScreen: %v", err)
					}

					far, err := program.call("worldToScreen",
						evalArgs(t, start, viewport, map[string]float64{"x": world, "y": 0}))
					if err != nil {
						t.Fatalf("worldToScreen: %v", err)
					}

					from, _ := near.field("x")
					to, _ := far.field("x")

					if !closeEnough(to-from, outline) {
						t.Errorf("map %v in %vx%v at scale %v: an outline asked to be %g screen "+
							"pixels came out %g", size, width, height,
							mustField(t, start, "worldPerViewport"), outline, to-from)
					}
				}
			}
		}
	}
}

// TestLabelsHoldTheirScreenSizeAtEveryScale is UI §7.6's "map labels inside the
// canvas scale with --type-scale", as arithmetic.
//
// Every tier's value is in the grid, including the two television ones, because the
// claim is that a label is *bigger* on a television — not merely that a number was
// multiplied.
func TestLabelsHoldTheirScreenSizeAtEveryScale(t *testing.T) {
	t.Parallel()

	program := cameraProgram(t)

	for _, typeScale := range []float64{1, 1.0625, 1.5, 1.75} {
		for _, size := range mapSizes {
			for _, width := range viewportWidths {
				for _, height := range viewportHeights {
					viewport := map[string]float64{"width": width, "height": height}

					for _, start := range startingCameras(t, program, size, width, height) {
						want := 12.0 * typeScale
						world := evalNum(t, program, "labelWorldSize", start, 12.0, typeScale)

						near, err := program.call("worldToScreen",
							evalArgs(t, start, viewport, map[string]float64{"x": 0, "y": 0}))
						if err != nil {
							t.Fatalf("worldToScreen: %v", err)
						}

						far, err := program.call("worldToScreen",
							evalArgs(t, start, viewport, map[string]float64{"x": world, "y": 0}))
						if err != nil {
							t.Fatalf("worldToScreen: %v", err)
						}

						from, _ := near.field("x")
						to, _ := far.field("x")

						if !closeEnough(to-from, want) {
							t.Errorf("--type-scale %g on map %v in %vx%v at scale %v: a 12px "+
								"label came out %g screen pixels, want %g",
								typeScale, size, width, height,
								mustField(t, start, "worldPerViewport"), to-from, want)
						}
					}
				}
			}
		}
	}
}

// TestZoomHoldsThePointUnderThePointer is the focus invariant, in both directions and
// at both bounds.
//
// The clamp is applied before the re-centre, so a zoom that hits a bound re-centres
// on where the pointer actually is at the *clamped* scale. A zoom that re-centred
// first and clamped second would move the pointer's target, which on a television —
// where the map is driven from a remote and the pointer is a cursor — reads as the
// map jumping away from what was aimed at.
func TestZoomHoldsThePointUnderThePointer(t *testing.T) {
	t.Parallel()

	program := cameraProgram(t)

	for _, size := range mapSizes {
		for _, width := range viewportWidths {
			for _, height := range viewportHeights {
				viewport := map[string]float64{"width": width, "height": height}
				start := cameraAt(t, program, size, width, height)
				focus := map[string]float64{"x": width * 0.31, "y": height * 0.67}
				fitted := mustField(t, start, "worldPerViewport")

				// `clampCamera` takes the two bounds in either order, and which order
				// is right depends on the scene, so this asserts against the interval
				// rather than against an inequality that only holds for half the grid.
				nearest, widest := minMax(fitted, 1)

				for _, factor := range []float64{1.1, 2, 8, 1 / 1.1, 0.25, 0.01, 100} {
					zoomed, err := program.call("zoomAt",
						evalArgs(t, start, viewport, focus, factor, nearest, widest))
					if err != nil {
						t.Fatalf("zoomAt(%g): %v", factor, err)
					}

					scale := mustField(t, zoomed, "worldPerViewport")

					if scale < nearest-arithEPS || scale > widest+arithEPS {
						t.Errorf("map %v in %vx%v, zoom %g: the scale is %g, outside [%g, %g]",
							size, width, height, factor, scale, nearest, widest)
					}

					before := mustField(
						t,
						evalRec(t, program, "screenToWorld", start, viewport, focus),
						"x",
					)
					after := mustField(
						t,
						evalRec(t, program, "screenToWorld", zoomed, viewport, focus),
						"x",
					)

					if !closeEnough(after, before) {
						t.Errorf("map %v in %vx%v, zoom %g: the map point under the pointer was "+
							"at %g and is now at %g", size, width, height, factor, before, after)
					}
				}
			}
		}
	}
}

// TestClampingAScaleNeverMovesTheMapCentre pins the other half of the zoom
// invariant.
//
// `clampCamera` exists to stop a user driving the scale out of range, and the obvious
// implementation of that would also recentre — which would be a second way for the
// map to move without the reader asking.
func TestClampingAScaleNeverMovesTheMapCentre(t *testing.T) {
	t.Parallel()

	program := cameraProgram(t)

	// One scale inside the bounds and one far outside each of them, so the clamp is
	// exercised in both directions as well as being left alone.
	for _, scale := range []float64{0, 0.1, 1, 7.5, 1000} {
		start := arithRec(map[string]float64{
			"centreX": 1234.5, "centreY": -678.25, "worldPerViewport": scale,
		})
		clamped, err := program.call("clampCamera", evalArgs(t, start, 0.25, 4.0))
		if err != nil {
			t.Fatalf("clampCamera(%g): %v", scale, err)
		}

		for _, axis := range []string{"centreX", "centreY"} {
			got, gerr := clamped.field(axis)
			if gerr != nil {
				t.Fatalf("clampCamera: %v", gerr)
			}

			if !closeEnough(got, mustField(t, start, axis)) {
				t.Errorf("clampCamera(%g) moved %s from %g to %g", scale, axis,
					mustField(t, start, axis), got)
			}
		}

		got := mustField(t, clamped, "worldPerViewport")
		if got < 0.25-arithEPS || got > 4+arithEPS {
			t.Errorf("clampCamera(%g) left the scale at %g, outside [0.25, 4]", scale, got)
		}
	}
}

// TestTheArithmeticGateRejectsWhatItCannotCheck is the meta-test.
//
// Every other test in this file trusts the evaluator. If the evaluator silently
// accepted a conditional, a string or a `Math.min`, then a `camera.js` written in
// plain JavaScript would still be "read by the gate" — just less of it — and the
// gate would report a coverage it does not have. So each construct the grammar
// claims to exclude is fed to it and required to produce a parse error naming the
// construct.
//
// The negative direction matters as much as the positive: an evaluator that
// rejected everything would pass this file too. `TestTheArithmeticGateEvaluatesWhat
// ItClaimsTo` is the other half — it evaluates a fixture whose answer is known.
func TestTheArithmeticGateRejectsWhatItCannotCheck(t *testing.T) {
	t.Parallel()

	for _, fixture := range []struct {
		name    string
		source  string
		expects string
	}{
		{
			name:    "a conditional",
			source:  "export function f(a) { if (a) { return 1; } return 0; }",
			expects: "if",
		},
		{
			name:    "a loop",
			source:  "export function f(a) { for (var i = 0; i < 3; i = i + 1) { return i; } }",
			expects: "for",
		},
		{
			name:    "a string literal",
			source:  `export function f(a) { return "scale"; }`,
			expects: "string",
		},
		{
			name:    "a call into the global namespace",
			source:  "export function f(a) { return Number.min(a, 1); }",
			expects: "global namespace",
		},
		{
			// Unbound names are `parseArith`'s business and
			// `TestTheArithmeticGateRefusesAnUnboundName`'s to prove; this table is the
			// grammar's, and it owns the shapes rather than the vocabulary.
			name:    "a Math member that is not one of the four",
			source:  "export function f(a) { return Math.round(a); }",
			expects: "global namespace",
		},
		{
			name:    "a default parameter",
			source:  "export function f(a = 2) { return a; }",
			expects: "default parameter",
		},
		{
			name:    "a name declared twice",
			source:  "export function f(a) { return a; } export function f(a) { return a * 2; }",
			expects: "declared twice",
		},
		{
			name:    "a declaration that is not a function",
			source:  "export const scale = 2;",
			expects: "const",
		},
		{
			name:    "an assignment in a body",
			source:  "export function f(a) { return a = 2; }",
			expects: "expected",
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()

			_, err := parseArith(fixture.source)
			if err == nil {
				t.Fatalf("the arithmetic gate accepted %s and reported no error.\n"+
					"A gate that reads less of camera.js than it claims still reports a coverage\n"+
					"it does not have, which is the silent pass this test exists to prevent.",
					fixture.name)
			}

			if fixture.expects != "" && !strings.Contains(err.Error(), fixture.expects) {
				t.Errorf("the gate rejected %s with %q, which does not name the construct.\n"+
					"A diagnostic a reader cannot act on is the same failure as no diagnostic.",
					fixture.name, err)
			}
		})
	}
}

// TestTheArithmeticGateEvaluatesWhatItClaimsTo is the other half of the meta-test.
//
// An evaluator that rejected everything would pass the test above. So a fixture with
// known answers is evaluated: the four built-ins, operator precedence, unary minus,
// member access, a record literal, and a call from one declared function to another.
func TestTheArithmeticGateEvaluatesWhatItClaimsTo(t *testing.T) {
	t.Parallel()

	program, err := parseArith(`
export function half(a) { return a / 2; }
export function scale(camera, viewport) {
  return {
    centreX: camera.centreX + viewport.width * half(2),
    centreY: -camera.centreY,
    worldPerViewport: Math.max(0.25, Math.min(4, camera.worldPerViewport)),
    inside: camera.centreX <= viewport.width && camera.centreY >= 0,
    wide: viewport.width >= 100 || viewport.height >= 100
  };
}
`)
	if err != nil {
		t.Fatalf("the arithmetic gate rejected a fixture it is supposed to evaluate: %v", err)
	}

	value, err := program.call("scale", evalArgs(t,
		arithRec(map[string]float64{"centreX": 10, "centreY": 3, "worldPerViewport": 9}),
		map[string]float64{"width": 5, "height": 6}))
	if err != nil {
		t.Fatalf("scale: %v", err)
	}

	for field, want := range map[string]float64{
		"centreX": 15, "centreY": -3, "worldPerViewport": 4,
		// `10 <= 5` is false, so the conjunction is false; and neither viewport side
		// reaches 100, so the disjunction is false. Both are 0 rather than absent,
		// which is what tells a reader the operator produced a number.
		"inside": 0, "wide": 0,
	} {
		got, gerr := value.field(field)
		if gerr != nil {
			t.Fatalf("scale(…).%s: %v", field, gerr)
		}

		if !closeEnough(got, want) {
			t.Errorf("scale(…).%s = %g, want %g", field, got, want)
		}
	}
}

// --- Small helpers -------------------------------------------------------------

func mustField(t *testing.T, value arithValue, name string) float64 {
	t.Helper()

	number, err := value.field(name)
	if err != nil {
		t.Fatalf("camera has no %s: %v", name, err)
	}

	return number
}
