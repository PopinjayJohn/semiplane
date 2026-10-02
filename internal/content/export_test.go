package content

// Test-only seams, following `internal/httpapi/auth/export_test.go`.
//
// `testpackage` requires the tests to live in `package content_test`, and the two
// things `target_test.go` needs to reach are unexported on purpose — ADR 0037's
// confinement claim is that nothing outside this package can reach them, and an
// exported function would make that claim a matter of discipline rather than of the
// compiler. So the tests get them through a file that only exists in a test build.

// ApplyTargetClass exposes applyTargetClass to the black-box tests.
//
// Why a test needs it rather than going through `Render`: `Render` takes markdown,
// and goldmark never lets a `<button>`, a `<select>`, a `<textarea>` or a `summary`
// reach the sanitiser. Three of those five are in UI §10.6's list of interactive
// elements, and the pass's contract is that the element set is **fixed** rather than
// tracking what a given render happened to produce — a set that shrinks with the
// policy is a set somebody has to remember, and the failure of forgetting is a live
// focus stop below the minimum. Testing the set therefore needs to reach the pass
// with a fragment the pipeline cannot produce.
//
// Everything a *reader* can observe is asserted through `Render`, and nothing here
// widens what the binary exports.
func ApplyTargetClass(page string) (string, error) {
	return applyTargetClass(page)
}

// RenderSanitised runs the pipeline **without** the `.target` pass, so a test can
// compare a rendered body against the body it is supposed to have come from.
//
// It is the strongest statement of "the pass writes nothing but a class token" that
// can be made about the *real* corpus rather than one hand-written fragment: render
// every golden document both ways and compare the two documents attribute by
// attribute.
//
// It delegates rather than repeating `Render`'s first four steps, which is the point
// — a second copy would have been a second copy that could drift. The comparison test
// is still load-bearing: it is what says the pass changed only the class lists, and it
// is the test that would notice if `Render` grew a step `RenderSanitised` does not
// share.
//
// The alternative — asserting on the class lists in the golden files alone — would
// pass on a pass that also added an attribute nobody thought to forbid.
func RenderSanitised(renderer *Renderer, document Document) (string, error) {
	result, err := renderer.sanitised(document)
	if err != nil {
		return "", err
	}

	return result.HTML, nil
}

// Sanitise exposes the render pipeline's sanitiser to the black-box tests, on its
// own.
//
// It exists for one assertion that cannot be made any other way: **that `target` is
// not on the `class` allowlist.** ADR 0037 rests on the class being semiplane's and
// applied by semiplane, and the negative of that is a property of `policy.go` — a
// regexp, in a `var` block, that a test can only reach through the policy itself.
// Asserting it from the far end does not work: goldmark drops raw HTML before the
// sanitiser sees it, so *every* `target` in a rendered page is semiplane's own and an
// output assertion cannot tell a policy that forbids the class from one that allows
// it and never uses it.
//
// The policy is built fresh rather than read off a `Renderer` because there is no
// exported accessor for one and adding one would be a wider door than a test needs.
func Sanitise(markup string) string {
	return newPolicy().Sanitize(markup) //nolint:misspell // bluemonday's own spelling.
}
