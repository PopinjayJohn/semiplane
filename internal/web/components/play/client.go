package play

// The client scripts the play page carries, and how it carries them.
//
// # Inline, and why
//
// UI §3.7's rule is that the only JavaScript on the critical path is the head
// resolver. These are not on the critical path — they run on `DOMContentLoaded`,
// after the first paint — and they are inline rather than `<script src>` for two
// reasons, and the first is the one that decided it:
//
//   - **A `src` would 404.** `/assets/` serves `web.Dist()`, which is
//     `static/dist/`, and the Makefile stages `static/css/` into it and nothing
//     stages `static/js/`. A module tree that is never copied is a script tag that
//     resolves to a 404, and a 404 script is a *silently* absent behaviour: the
//     page renders, the token list is operable by `Tab`, and the arrow keys and
//     D16's remembered tab are simply absent. The Makefile is the integrator's
//     and this work item does not own it, so the dependency is reported rather
//     than taken.
//
//   - **Inlining removes a round trip** for four kilobytes of script on a page
//     that already has one connection doing something more important.
//
// The cost is the second copy: a file to edit and a constant to serve.
// `internal/web/static/js/tokens` owns the file and `go:embed` carries the bytes,
// so the "two copies" risk is a *file and its own compiled bytes*, and
// `TestTheServedScriptsAreTheCheckedInScripts` compares them.
//
// # A raw script body, guarded rather than trusted
//
// templ escapes text, and escaping a script body is worse than useless — `&lt;`
// is not an operator — so the body is written raw. That is the only correct thing
// to do and also the only dangerous thing to do, so `writeScript` checks it: a
// script body is terminated by the HTML parser at `</script` and at `<!--`, and a
// module containing either would end the element and turn the rest of the script
// into markup in every page of the product. `checkInert` is a constant scan, so it
// costs nothing per request, and it fails the build rather than a browser.
//
// This mirrors `internal/httpapi/shell.writeScript`, which this work item does not
// own. Duplicated rather than exported from there because that package is not this
// work item's file, and a one-line export request is a smaller change than a
// second copy of a security check is a risk. The report names it.

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/a-h/templ"

	"github.com/semiplane/semiplane/internal/web/static/js/live"
	mapjs "github.com/semiplane/semiplane/internal/web/static/js/map"
	"github.com/semiplane/semiplane/internal/web/static/js/tokens"
)

// ClientScripts writes the tabletop's modules, in order, as inline script
// elements.
//
// **One component rather than three script elements at each call site**, for the
// reason `shell.Resolver` is one: the order is a dependency (`tokens.js` calls
// `spFocusStep`, defined in `step.js`) and an order somebody has to remember at
// four call sites is an order that eventually is wrong.
func ClientScripts() templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, out io.Writer) error {
		// The `tokens` tree is **inlined**, because its three files are IIFEs with
		// no `import` and no `export` and a classic inline script can carry them.
		for _, name := range tokens.Order() {
			if err := writeScript(ctx, out, tokens.Source(name)); err != nil {
				return fmt.Errorf("write %s: %w", name, err)
			}
		}

		// The `live` and `map` trees are **referenced by URL**, and the difference
		// is not a preference: they are ES modules, and `import`/`export` are a
		// syntax error in a classic script. Inlining `chrome.js` would produce a
		// document whose script element the browser refuses to parse, which is a
		// page that looks fine and does nothing -- so `type="module"` and `src`.
		//
		// **These were absent entirely until C6 caught it.** `client.go` wrote the
		// resolver and the three token modules and nothing else, so the document
		// mounted all five `data-chrome` patch targets and opened no socket, no
		// event stream and no canvas. Every layer's own test passed, because each
		// one audits bytes that somebody else has to reference, and
		// `components/play`'s own test asserted *three* script elements for three
		// modules -- a number that was an artefact of there being one tree, and
		// therefore a test that could not notice the other two appearing.
		//
		// `defer` rather than `async`: a module script is deferred by default, and
		// saying so is cheaper than relying on it. The order between the two trees
		// is not a dependency -- they share no import -- and within each it is,
		// which is why they are separate elements in their package's own `Order()`.
		for _, tree := range []struct {
			order []string
			href  func(string) string
			kind  string
		}{
			{live.Order(), live.Href, "live chrome"},
			{mapjs.Order(), mapjs.Href, "map canvas"},
		} {
			for _, name := range tree.order {
				if err := writeModuleScript(ctx, out, tree.href(name)); err != nil {
					return fmt.Errorf("write %s module %s: %w", tree.kind, name, err)
				}
			}
		}

		return nil
	})
}

// The sequences that end a script body.
//
// `</script` closes the element. `<!--` opens a comment state inside a script
// that only `</script` or `-->` leaves, and a `-->` outside one is stray markup.
// None appears in these modules today; all three are checked because a module is
// a hand-maintained file and the failure would be an XSS in every play page.
var inertnessViolations = []string{"</script", "<!--", "-->"}

// checkInert reports whether raw source is safe inside a script element.
func checkInert(source string) error {
	lowered := strings.ToLower(source)

	for _, violation := range inertnessViolations {
		if strings.Contains(lowered, violation) {
			return fmt.Errorf("inline script contains %q, which ends a script element", violation)
		}
	}

	return nil
}

// writeScript writes one inline script element around raw source.
//
// The nonce is honoured for the reason `shell.writeScript` documents: a strict
// `Content-Security-Policy` without `'unsafe-inline'` refuses an inline script
// carrying no nonce, so a route that has one puts it on the context with
// `templ.WithNonce` before rendering. Omitted rather than rendered empty, because
// `nonce=""` is not the same as no nonce to a CSP check.
// writeModuleScript writes one `<script type="module" src="…">`.
//
// **By reference and never inline**, because a module's `import` and `export` are
// a syntax error in a classic script. The nonce rides along because a strict CSP
// without `'unsafe-inline'` refuses an inline script carrying none, and the shell
// writer documents the same reasoning for its own element.
//
// The order written is the document's order, which is the module graph's:
// `table.js` imports the other three, and a module graph the browser resolves
// twice is a graph that loads twice.
func writeModuleScript(ctx context.Context, out io.Writer, href string) error {
	if _, err := io.WriteString(out, `<script type="module"`); err != nil {
		return fmt.Errorf("open the module script element: %w", err)
	}

	if nonce := templ.GetNonce(ctx); nonce != "" {
		if _, err := fmt.Fprintf(out, ` nonce=%q`, nonce); err != nil {
			return fmt.Errorf("write the module script nonce: %w", err)
		}
	}

	if _, err := fmt.Fprintf(out, ` src=%q></script>`, href); err != nil {
		return fmt.Errorf("write the module script src: %w", err)
	}

	return nil
}

func writeScript(ctx context.Context, out io.Writer, source string) error {
	if err := checkInert(source); err != nil {
		return err
	}

	if _, err := io.WriteString(out, `<script`); err != nil {
		return fmt.Errorf("open script element: %w", err)
	}

	if nonce := templ.GetNonce(ctx); nonce != "" {
		if _, err := fmt.Fprintf(out, ` nonce=%q`, nonce); err != nil {
			return fmt.Errorf("write script nonce: %w", err)
		}
	}

	if _, err := io.WriteString(out, `>`+source+`</script>`); err != nil {
		return fmt.Errorf("write script body: %w", err)
	}

	return nil
}
