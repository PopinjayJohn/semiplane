// The redaction seam: the one place content the viewer may not see leaves a
// page, and the position that removal runs at is the security property.
//
// S-5.7 states it in one sentence — redaction happens before sanitisation and
// before the value reaches any template, so no intermediate buffer holds an
// unredacted copy for a non-GM — and this file exists because the sentence is
// otherwise easy to satisfy in the wrong order. Everything after it in the
// pipeline is cheap to reorder and expensive to get right:
//
//   - Redact after the render and the unredacted body has been through three
//     buffers this package does not control — goldmark's, bluemonday's, and the
//     cache's — each of which is a thing a bug, a log line, a core dump or a
//     future phase can read from.
//   - Redact inside the template and the value has crossed a sanitiser whose
//     whole job is to be the last thing that touches author-supplied HTML, so
//     the two controls would have to be reasoned about together.
//   - Redact before the render, on the source, and the non-GM pipeline never
//     constructs the string. There is nothing to leak because there is nothing
//     there.
//
// So the interface is expressed on the *source* text, `Redact(body string, …)`,
// and a caller cannot reach it late in the pipeline without changing the type of
// the value it holds. The wiki route's `source` and `redacted` types make that
// ordering structural rather than conventional; this file is the contract that
// makes it expressible.

package content

// Redactor removes content the viewer may not see, operating on the *source*
// text before the render.
//
// The position is the security property, not an implementation detail. A
// redactor that ran after the render would mean the unredacted body existed in
// the renderer's buffers, the sanitiser's input, and possibly a cache entry —
// three places a bug, a log line or a core dump would leak it from. Running on
// the source means the non-GM pipeline never constructs the string at all.
//
// An interface rather than a function value so that a caller cannot build the
// pipeline with a redactor chosen at the point of use, and so a test can
// substitute one that removes a known marker — which is the only way to assert
// S-14.1's property, which is that the text appears *nowhere* rather than that
// one function returned something falsy.
type Redactor interface {
	// Redact returns body with viewer-invisible content removed, when
	// includeSecrets is false. With includeSecrets true it returns body
	// unchanged.
	//
	// The whole body, not a rendered fragment and not a front-matter value: a
	// redactor that could only see the prose could not remove anything an author
	// wrote in a field, and a redactor that could only see the rendered HTML
	// would be running one step too late.
	//
	// includeSecrets is the caller's own decision about the viewer, and it is
	// never an input from the request. See the wiki route for where it comes
	// from; the short form is that a query parameter, a cookie or a header would
	// each be a way for a reader to ask for text they may not have.
	Redact(body string, includeSecrets bool) (string, error)
}

// noSecrets is the Redactor below. A type rather than a package-level value so
// there is no mutable state for one to share, and so a caller cannot compare
// against it to learn which redactor is installed.
type noSecrets struct{}

// NoSecrets is the redactor this phase installs: it removes nothing.
//
// Correct for this phase and wrong for P10, and it is wrong in a way worth
// naming — a page containing `[!secret]` text renders in full to every viewer
// until P10 replaces this. The name reads like a safety guarantee and is the
// opposite of one: it says there is no mechanism here yet, not that nothing can
// be hidden by mistake. P10 replaces the body of `Redact` and nothing else —
// the signature, the position and this file stay.
//
// A function rather than a `var` so that installing it is visible at the call
// site in the composition root, and so nothing in the process can swap it after
// the pipeline has been built.
func NoSecrets() Redactor {
	return noSecrets{}
}

// Redact returns body unchanged.
//
// Both parameters are accepted and neither is read, and that is the whole
// implementation: this phase has no secret syntax, so there is nothing this
// phase may remove. It is deliberately *not* an error to be asked, and it is
// deliberately not conditional on the viewer — a redactor whose behaviour
// depended on who was asking would be a second place to get `includeSecrets`
// wrong, and the whole point is that the seam has exactly one such value.
func (noSecrets) Redact(body string, _ bool) (string, error) {
	return body, nil
}
