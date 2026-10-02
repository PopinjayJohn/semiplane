package shell

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/a-h/templ"
)

// ResolverMaxBytes is the budget for the blocking script, in bytes.
//
// UI §3.7 asks for "inline, synchronous, under ~1KB", and the number is here
// rather than only in a test so a reader of this file sees the budget the way
// they see any other constant. It is a hard gate: the blocking script is parsed
// and executed on every page load, before anything paints, so every byte of it
// is added to the time to first paint of every route in the product.
const ResolverMaxBytes = 1024

// The served script, byte for byte the same text as
// `internal/web/static/js/head.js`.
//
// The file is the one a person edits — it is the script, not a string, and a
// syntax error in it is a syntax error the file catches. This constant is what
// the server sends, and it has to be a constant because the script is inlined:
// an external file would be a second round trip on the critical path, which is
// exactly what §3.7 forbids. Two copies of the same text is a drift risk, so
// `TestTheCheckedInScriptIsTheServedScript` compares them byte for byte and
// fails the build when they diverge. Nothing in the file is generated from this
// constant, and nothing in this constant is generated from the file.
//
// The separator between the cookie's two fields is `&`, and it has to be.
//
// `auth.WriteUIPreferences` writes `"theme=" + theme + "; ui=" + mode`, and that
// value cannot survive a trip through a cookie: `;` is the cookie-pair
// separator, so `net/http` refuses the value on the way out, `document.cookie`
// hands it back as two unrelated cookies, and the server's own request parser
// sees a `sp_ui` with no `ui` at all. No separator that `auth` currently
// splits on can work, because the only separator it splits on is the one the
// cookie grammar reserves. `&` is an ordinary cookie-octet: it round-trips
// through `http.SetCookie`, through a `Cookie:` request header and through
// `document.cookie` unchanged, which is what the resolver's tolerant `/[;&\s]+/`
// split is there for — a cookie written by the current `auth.WriteUIPreferences`
// arrives as `theme=dark ui=tv` with the separator already stripped, and the
// resolver still reads both fields out of it.
//
// `auth.parseUIPreferences` therefore needs to split on `&` where it splits on
// `;`, and `auth.WriteUIPreferences` needs to write the same. That file is not
// this work item's, so the change is reported rather than made.
const (
	resolverSource = `/* semiplane head resolver — UI §3.7 */
(function(){var d=document,e=d.documentElement,M=/^(auto|laptop|phone|tv)$/,T=/^(light|dark)$/,H=/SmartTV|Tizen|webOS|HbbTV|Web0S|NetCast|AppleTV/,S=/[;&\s]+/,q=new URLSearchParams(location.search).get("ui"),c=d.cookie.match(/(?:^|;\s*)sp_ui=([^;]*)/),p={theme:"auto"},i,x,f,o=1;if(c)for(i of c[1].split(S)){x=i.split("=");if(x.length>1)p[x[0]]=x[1]}p.theme=T.test(p.theme)?p.theme:"auto";f=M.test(q||"")?q:M.test(p.ui||"")?p.ui:"";if(!f&&!c&&H.test(navigator.userAgent))f="tv";function t(){var w=innerWidth,h=innerHeight;return f=="tv"?w>=2200?"tv-wide":"tv":f=="phone"||w<1024?h<=480?"compact-short":"compact":w<1280?"medium":w<1536?"large":"wide"}function a(){e.dataset.ui=t();e.dataset.theme=p.theme=="auto"?(matchMedia("(prefers-color-scheme:dark)").matches?"dark":"light"):p.theme;if(f&&o)d.cookie="sp_ui=theme="+p.theme+"&ui="+f+"; Path=/; Max-Age=31536000; SameSite=Lax"}e.spUI=function(u,c){if(M.test(u))f=u;T.test(c)&&(p.theme=c);a()};e.spTier=a;a();o=0})();` //nolint:misspell // CSS keyword.

	sheetScriptSource = `/*
 * Part 2 — the sheet re-parent. It cannot be part 1, and that is the whole
 * reason it is a second element rather than a second statement: at the moment
 * the resolver runs there is no document.body, so there is nowhere to move
 * anything to. The DOM does not exist yet, which is why the tier lives on the
 * document element (available immediately) and the movement waits for parse.
 *
 * Moving a node is not re-rendering it (UI §3.7's last task, UI §3.5's
 * "reflows with no modal, no reload, no loss of editor buffer"). Nothing is
 * fetched, nothing is rebuilt, and a moved panel keeps its scroll offset. The
 * recorded home is the parent and the next sibling rather than an index,
 * because a panel's recorded sibling is always a non-panel: in the shell the
 * order is header, nav, main, rail, footer, so each panel's next sibling is
 * the element after it. Focus is captured and reapplied because §7.9 requires
 * an orientation change to preserve it.
 */
(function(){function M(){var d=document,e=d.documentElement,h=d.createElement("div"),n=e.querySelectorAll("[role=navigation],[role=complementary]"),a=[];h.className="shell-sheets";d.body.append(h);n.forEach(function(x){a.push([x,x.parentNode,x.nextSibling])});return function(){var f=d.activeElement;e.spTier&&e.spTier();a.forEach(function(r){var s=e.dataset.ui.indexOf("compact")==0?h:r[1],q=s===h?null:r[2];if(r[0].parentNode!=s||r[0].nextSibling!=q)s.insertBefore(r[0],q)});if(f&&f.focus)f.focus()}}addEventListener("DOMContentLoaded",function(){var r=M();r();addEventListener("resize",r)})})();`
)

// ResolverSource returns the blocking script's exact served text.
//
// Exported for the audit and for the drift test, and for nothing else: a caller
// that wanted to run the resolver itself would be building a second code path
// around a document the server has already sent.
func ResolverSource() string {
	return resolverSource
}

// SheetScriptSource returns the post-parse script's exact served text.
//
// The second half of the same file. It exists because the first half cannot do
// the sheet re-parenting: at the moment it runs there is no `document.body`, so
// there is nowhere to move a panel to. It is not on the critical path, which is
// why §3.7's "only JavaScript on the critical path" stays true of the pair.
func SheetScriptSource() string {
	return sheetScriptSource
}

// Resolver returns the head's inline scripts, in the order they must run.
//
// One call site, two elements, so the ordering is this function's problem and
// not every template's. The first is the blocking resolver; the second is the
// sheet re-parent, which cannot start before the document has been parsed.
//
// The context is honoured for the CSP nonce and for nothing else: a strict
// `Content-Security-Policy` without `'unsafe-inline'` refuses an inline script
// that carries no nonce, so a route that has one puts it on the context with
// `templ.WithNonce` before rendering. Nothing in the product sets a
// `Content-Security-Policy` yet, and adding that header without a nonce
// generator is what would break the shell — the plumbing is here so that the
// header can arrive without anyone having to edit this file.
func Resolver() templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, out io.Writer) error {
		if err := writeScript(ctx, out, resolverSource); err != nil {
			return fmt.Errorf("write head resolver: %w", err)
		}

		if err := writeScript(ctx, out, sheetScriptSource); err != nil {
			return fmt.Errorf("write sheet re-parent script: %w", err)
		}

		return nil
	})
}

// writeScript writes one inline script element around raw source.
//
// The source is written unescaped, which is the only correct thing to do and
// also the only dangerous thing to do, so it is guarded rather than trusted: a
// script body is terminated by the parser at `</script` and at `<!--`, and a
// constant containing either would end the element and turn the rest of the
// script into markup. The check is a constant scan, so it costs nothing per
// request, and it fails the build rather than a browser.
func writeScript(ctx context.Context, out io.Writer, source string) error {
	if err := checkInert(source); err != nil {
		return err
	}

	if _, err := io.WriteString(out, `<script`); err != nil {
		return fmt.Errorf("open script element: %w", err)
	}

	// Omitted entirely when the context carries no nonce, rather than rendered
	// as an empty attribute: `nonce=""` is not the same as no nonce to a CSP
	// check, and an empty one reads as a bug to the next person looking.
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

// inertnessViolations are the sequences that end a script body.
//
// `</script` closes the element. `<!--` opens a comment state inside a script
// that only `</script` or `-->` leaves, and a `-->` outside one is stray markup.
// None appears in the resolver today; all three are checked because the
// resolver is a hand-maintained constant and the failure would be an XSS in
// every page of the product.
var inertnessViolations = []string{"</script", "<!--", "-->"}

// checkInert reports whether raw source is safe to place inside a script
// element unescaped.
func checkInert(source string) error {
	lowered := strings.ToLower(source)

	for _, violation := range inertnessViolations {
		if strings.Contains(lowered, violation) {
			return fmt.Errorf("inline script contains %q, which ends a script element", violation)
		}
	}

	return nil
}

// The two spellings the HTML and CSS specifications fix, in one place each.
//
// A spell-checker configured for British English objects to the American form
// wherever it finds it, and it cannot be allowed to rewrite the name of a
// standard. Holding each in a constant means no comment in this package has to
// spell either word at all, which is a better outcome than a `nolint` per
// occurrence: a suppression that has to be repeated is one somebody eventually
// deletes while tidying.
//
// The element is written as a constant rather than assembled from parts at the
// call site, because the whole point of it is that it is the exact bytes a
// browser reads, and a concatenation is one edit away from an attribute with a
// stray space in it.
const (
	colorSchemeName = "color-scheme"                                                //nolint:misspell // A CSS property and an HTML attribute name.
	colorSchemeMeta = `<meta name="` + colorSchemeName + `" content="light dark"/>` //nolint:misspell // As above.
)

// ColorSchemeMeta returns the colour-scheme meta element.
//
// §3.7 requires it, and its whole job is the visitor with no script: the
// browser paints form controls, scrollbars and the canvas in the operating
// system's polarity, so a page that declares nothing is a light page with dark
// scrollbars. It costs seven attributes and it is the difference between losing
// a preference and losing coherence.
//
// The template already contains this element literally, and it should keep
// doing so — a second copy in a Go constant would be a second answer to a
// question with one right value. It is exposed here so a head assembled
// outside `components.Shell` has the same answer available rather than
// inventing one.
func ColorSchemeMeta() templ.Component {
	return templ.ComponentFunc(func(_ context.Context, out io.Writer) error {
		_, err := io.WriteString(out, colorSchemeMeta)
		if err != nil {
			return fmt.Errorf("write the colour-scheme meta: %w", err)
		}

		return nil
	})
}
