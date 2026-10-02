package shell_test

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"

	"github.com/semiplane/semiplane/internal/config"
	"github.com/semiplane/semiplane/internal/httpapi"
	accountroutes "github.com/semiplane/semiplane/internal/httpapi/accounts"
	"github.com/semiplane/semiplane/internal/httpapi/auth"
	"github.com/semiplane/semiplane/internal/httpapi/shell"
	"github.com/semiplane/semiplane/internal/observability"
	"github.com/semiplane/semiplane/internal/web/components"
)

// These tests are against the *served bytes*, and that is the whole design of
// the package they cover.
//
// The property S-13.5 rests on is that the document never varies by `sp_ui`, and
// the only way to hold it is to look at what a request actually returns. A test
// that reads the source of a `.templ` file proves the template does not say
// `data-ui`; it does not prove the response does not carry it, because an
// attribute can arrive from a partial, from a layout, or from a comment — and a
// `data-ui` inside an HTML comment is invisible to a substring test and very
// much present in the document a cache stores.

// servedDocument fetches a path from a router built with no store, which is the
// smallest router that renders a shell document.
func servedDocument(t *testing.T, path string, cookies ...*http.Cookie) (string, http.Header) {
	t.Helper()

	router := httpapi.NewRouter(
		slog.New(slog.DiscardHandler),
		config.Config{HandlerTimeout: time.Second},
		observability.NewRegistry(),
		// An account router with no store. Both pre-campaign routes are
		// reachable without one: an anonymous reader is shown the sign-in form
		// rather than redirected to it, so neither handler touches the store on
		// the paths under test. Supplying it is what makes these assertions
		// about a real served document rather than about a component.
		&accountroutes.Router{},
		nil,
		nil,
		// The four remaining campaign-scoped handlers, none of them wired. This
		// file audits the *pre-campaign* shell document, which is why they are all
		// nil: there is no campaign, so there is no content root to read, no index
		// to query, no page to edit and no stream to open. The arity is the only
		// thing this call has to track, and the router's own nil-tolerance is what
		// keeps a campaign route out of a pre-campaign document rather than a
		// panic at boot.
		nil,
		nil,
		nil,
		nil,
	)

	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, http.NoBody)
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200: %s", path, recorder.Code, recorder.Body.String())
	}

	return recorder.Body.String(), recorder.Header()
}

// uiCookie is an `sp_ui` cookie carrying a value, for a test that needs a
// browser to arrive holding one.
func uiCookie(value string) *http.Cookie {
	return &http.Cookie{Name: auth.UICookieName, Value: value}
}

// TestTheServedDocumentCarriesNeitherRootAttribute is S-13.5's negative half,
// and the phase-5 Definition of Done's sentence: *the served document contains
// neither `data-theme` nor `data-ui` before script execution*.
//
// Two attributes the server must never send, asserted on the parsed tree of the
// document a request returns, for every pre-campaign route that exists. If a
// route started server-rendering the tier — because a handler decided it knew
// better than the script — this is what catches it, and the consequence of not
// catching it is a cache serving one reader's television layout to everyone.
func TestTheServedDocumentCarriesNeitherRootAttribute(t *testing.T) {
	t.Parallel()

	// The routes plus the component itself. The component is here because the
	// routes are built from it: a route that renders a different document would
	// not be covered by testing the routes alone, and the component is the thing
	// a later phase edits.
	served := map[string]string{}

	for _, path := range []string{"/", "/login"} {
		document, _ := servedDocument(t, path)
		served[path] = document
	}

	direct, err := renderDocument(t, components.Shell(
		components.ShellView{Instance: components.InstanceView{}},
		templ.NopComponent,
		templ.NopComponent,
	))
	if err != nil {
		t.Fatalf("render shell component: %v", err)
	}

	served["components.Shell"] = direct

	for name, document := range served {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			audit := shell.AuditDocument(document)

			if len(audit.ResolvedAttributes) != 0 {
				t.Errorf(
					"served document carries %v; the tier and the theme are "+
						"resolved on the client and must not be sent, or the "+
						"document varies by a value the server cannot see "+
						"(UI §3.7, S-13.5):\n%s",
					audit.ResolvedAttributes, audit.Describe(),
				)
			}
		})
	}
}

// TestTheDocumentDoesNotVaryByTheUICookie is the property behind the absence: if
// two browsers holding different preferences receive the same bytes, then
// nothing in the response can depend on the cookie, and no `Vary` is owed.
//
// Asserted as a byte comparison rather than by reading the response for the
// cookie's value, because the failure this catches is a *new* dependency — one
// added by a later phase that has not been written yet — and the only way to
// notice a new dependency is to change the input and look at the output.
func TestTheDocumentDoesNotVaryByTheUICookie(t *testing.T) {
	t.Parallel()

	// Four preferences, chosen to differ in every field the cookie has: a
	// television in dark mode, a phone in light, an explicit auto, and a
	// malformed value. A handler that read either field and branched on it would
	// produce at least two distinct bodies across these.
	preferences := []string{
		"theme=dark&ui=tv",
		"theme=light&ui=phone",
		"theme=auto&ui=auto",
		"theme=neon&ui=television",
	}

	for _, path := range []string{"/", "/login"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			bodies := map[string]string{}

			for _, preference := range preferences {
				document, _ := servedDocument(t, path, uiCookie(preference))
				bodies[preference] = document
			}

			// And with no cookie at all, which is a fifth input: a handler that
			// branched on the cookie's *presence* rather than its value is the
			// same bug wearing a different hat.
			document, _ := servedDocument(t, path)
			bodies["no cookie"] = document

			for preference, body := range bodies {
				if body != bodies[preferences[0]] {
					t.Errorf(
						"GET %s with sp_ui=%q returned different bytes than with "+
							"sp_ui=%q; the document must not vary by the cookie "+
							"(UI §3.7, S-13.5)",
						path, preference, preferences[0],
					)
				}
			}
		})
	}
}

// TestNoShellResponseDeclaresVary asserts the absence as an absence.
//
// `Header.Values` rather than `Header.Get`: `Get` returns "" for both "not set"
// and "set to empty", so a `Vary:` with nothing after it would pass a `Get`
// check. There must be no `Vary` header on a shell response at all — not a
// `Vary: Cookie` and not a `Vary: Accept-Encoding` — because the shell document
// is one representation and a cache is free to store it.
//
// The wiki route does emit `Vary: Cookie`, and deliberately: that document
// carries the reader's name and a sign-out form, so it genuinely is
// reader-dependent. S-13.5 is about the *theme* cookie and this is about the
// shell, and the two must not be conflated — a future phase adding `Vary: Cookie`
// to the pre-campaign routes on the strength of "the wiki has one" would
// fragment every shared cache in front of the server over a value that changes
// nothing about the document (UI §6.6).
func TestNoShellResponseDeclaresVary(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/", "/login", "/healthz"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			_, headers := servedDocument(t, path, uiCookie("theme=dark&ui=tv"))

			if vary := headers.Values("Vary"); len(vary) != 0 {
				t.Errorf(
					"GET %s declared Vary: %q; a shell document is one "+
						"representation and no Vary is owed (UI §6.6, S-13.5)",
					path, vary,
				)
			}
		})
	}
}

// TestTheAuditFindsAResolvedAttributeWhereverItIs is the audit's own test, and
// it is the one that makes the other tests mean something.
//
// An audit that never fires is worse than no audit: it is a green test that
// asserts the absence of a thing nothing is looking for. So every way a resolved
// attribute can reach a served document is fed to the audit and the audit is
// required to object — including the case that motivates parsing the DOM at all,
// where the attribute is inside an HTML comment. A comment is invisible to a
// substring test and to a reader, and it is stored in every cache along with the
// rest of the document; a `data-ui` in one is a tier that half the readers
// apply and half do not.
func TestTheAuditFindsAResolvedAttributeWhereverItIs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		document string
		want     shell.RootAttribute
	}{
		{
			name:     "on the document element",
			document: `<html data-ui="tv"><head></head><body></body></html>`,
			want:     shell.AttributeUI,
		},
		{
			name:     "on the body, which is what a handler would most likely do",
			document: `<html><head></head><body data-theme="dark"></body></html>`,
			want:     shell.AttributeTheme,
		},
		{
			name:     "on a landmark, spelled in capitals",
			document: `<html><head></head><body><main DATA-UI="large"></main></body></html>`,
			want:     shell.AttributeUI,
		},
		{
			name: "in an HTML comment, which is the case a substring test misses",
			document: "<html><head></head><body>" +
				"<!-- data-ui=\"tv\" from an earlier build --></body></html>",
			want: shell.AttributeUI,
		},
		{
			name:     "in ordinary text, which is not an attribute and still varies the document",
			document: `<html><head></head><body><p>set data-ui=tv to try</p></body></html>`,
			want:     shell.AttributeUI,
		},
		{
			name: "on an element inside a comment, so the parser never builds a node for it",
			document: "<html><head></head><body>" +
				"<!--<div data-theme=\"light\"></div>--></body></html>",
			want: shell.AttributeTheme,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			audit := shell.AuditDocument(testCase.document)

			if len(audit.Faults) == 0 {
				t.Fatalf(
					"the audit reported no fault for a document carrying %s; an "+
						"audit that cannot fail is not an audit",
					testCase.want,
				)
			}

			if !containsAttribute(audit.ResolvedAttributes, testCase.want) {
				t.Errorf(
					"the audit reported %v, want it to name %s; a fault that does "+
						"not name the attribute is a fault nobody can act on",
					audit.ResolvedAttributes, testCase.want,
				)
			}
		})
	}
}

// TestTheAuditDoesNotFaultTheResolversOwnSource is the other half of the audit's
// test: the resolver's own text names both attributes many times, and the audit
// has to not report them.
//
// A naive text scan fails here, which is why the scan skips script and style
// bodies. Skipping them is safe precisely because an attribute cannot be *set*
// from inside a script: the HTML parser has already finished the element, and
// the script's writes happen through the DOM.
func TestTheAuditDoesNotFaultTheResolversOwnSource(t *testing.T) {
	t.Parallel()

	audit := shell.AuditDocument(headDocument(t))

	if len(audit.ResolvedAttributes) != 0 {
		t.Errorf(
			"the audit reported %v for a document carrying the resolver; the "+
				"resolver's source names both attributes, and its writes are DOM "+
				"writes rather than markup",
			audit.ResolvedAttributes,
		)
	}
}

// containsAttribute reports membership in a small list.
func containsAttribute(list []shell.RootAttribute, want shell.RootAttribute) bool {
	return slices.Contains(list, want)
}

// TestTheHeadTheResolverProducesSatisfiesTheAudit builds the head the way a
// template is meant to — meta, then the resolver, then the stylesheet — and
// audits the result. It is the check that goes green the moment `shell.templ`
// renders `shell.Resolver()`, and it is here rather than only there so the
// package's own output is covered today.
//
// The document is assembled in this test rather than rendered by a template
// because this work item does not own any `.templ` file. `shell.Resolver()` is
// the piece S5 adds, and the markup around it is the piece S5 owns; what is
// checked here is that the piece this package produces is correct on its own
// terms.
func TestTheHeadTheResolverProducesSatisfiesTheAudit(t *testing.T) {
	t.Parallel()

	assembled := headDocument(t)

	audit := shell.AuditDocument(assembled)

	if faults := audit.Describe(); faults != "no faults" {
		t.Errorf("the head shell.Resolver() produces does not satisfy the contract: %s", faults)
	}

	if !audit.HasColorSchemeMeta {
		t.Error("the assembled head has no colour-scheme meta")
	}

	if !audit.ResolverInHead {
		t.Error("the resolver is not inside <head>")
	}

	if !audit.ResolverBeforeStyles {
		t.Error("the stylesheet link precedes the resolver, so the first paint is unthemed")
	}
}

// TestTheResolverFitsItsBudget is UI §3.7's "inline, synchronous, under ~1KB",
// and it is a byte count rather than a reading of the source because the budget
// exists to bound parse time on every page load of the product.
func TestTheResolverFitsItsBudget(t *testing.T) {
	t.Parallel()

	if got := len(shell.ResolverSource()); got > shell.ResolverMaxBytes {
		t.Errorf(
			"the blocking resolver is %d bytes, over the %d-byte budget; it is "+
				"parsed and executed before anything paints (UI §3.7)",
			got, shell.ResolverMaxBytes,
		)
	}
}

// TestTheCheckedInScriptIsTheServedScript holds the two copies of the resolver
// to each other.
//
// The script is a checked-in file so a person edits JavaScript rather than a Go
// string, and it is a Go constant so it can be inlined — an external file would
// be a second round trip on the critical path, which §3.7 forbids. That gives
// two copies of the same text, and two copies of the same text drift. This
// compares them byte for byte, so the drift fails the build rather than
// production.
func TestTheCheckedInScriptIsTheServedScript(t *testing.T) {
	t.Parallel()

	checked := readScript(t)

	want := shell.ResolverSource() + "\n" + shell.SheetScriptSource() + "\n"
	if checked != want {
		t.Errorf(
			"internal/web/static/js/head.js and the served resolver have "+
				"diverged: the file is %d bytes, the served text is %d. The "+
				"constant is what the server sends; the file is what a person "+
				"edits. They must be the same bytes.",
			len(checked), len(want),
		)
	}
}

// TestTheInlineScriptCannotEndItsOwnElement is the check that lets the script
// be written unescaped.
//
// templ escapes text, and escaping a script body is worse than useless — `&lt;`
// is not an operator — so the body is written raw. A script body is terminated
// by the HTML parser at `</script` and at `<!--`, so a constant containing
// either would end the element and turn the rest of the script into markup in
// every page of the product. The resolver contains no such sequence today; the
// assertion is here so adding one is a failing test rather than an XSS.
func TestTheInlineScriptCannotEndItsOwnElement(t *testing.T) {
	t.Parallel()

	sources := map[string]string{
		"resolver":        shell.ResolverSource(),
		"sheet re-parent": shell.SheetScriptSource(),
	}

	for name, source := range sources {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			lowered := strings.ToLower(source)
			for _, violation := range []string{"</script", "<!--", "-->"} {
				if strings.Contains(lowered, violation) {
					t.Errorf(
						"the inline %s contains %q, which ends a script element; "+
							"the body is written unescaped, so the rest of it would "+
							"be parsed as markup", name, violation,
					)
				}
			}
		})
	}
}

// TestTheResolverWritesTheCookieWithTheAgreedAttributes pins the attributes the
// resolver writes, because they are a contract with every browser's cookie store
// and with `auth.WriteUIPreferences`.
//
// `Max-Age` and `SameSite` are named rather than compared loosely: the
// preference has to outlive a laptop's battery and must not ride a cross-site
// request, and both are one typo away from being wrong in a way nothing else
// would notice. `HttpOnly` is asserted *absent* for the reason `auth` gives —
// the resolver has to read this cookie before first paint — and `density` for
// §6.6's, which considered a density preference and did not select one.
func TestTheResolverWritesTheCookieWithTheAgreedAttributes(t *testing.T) {
	t.Parallel()

	source := shell.ResolverSource()

	for _, required := range []string{
		`"sp_ui=theme="`,
		`"&ui="`,
		`"; Path=/; Max-Age=31536000; SameSite=Lax"`,
	} {
		if !strings.Contains(source, required) {
			t.Errorf("the resolver does not write the cookie fragment %s", required)
		}
	}

	for _, forbidden := range []string{"HttpOnly", "Secure", "density", "; ui="} {
		if strings.Contains(source, forbidden) {
			t.Errorf(
				"the resolver writes %q; the cookie is two fields, is readable by "+
					"design, and carries no density (UI §6.6, auth.UICookieName)",
				forbidden,
			)
		}
	}
}

// TestTheCookieRoundTripsThroughACookieJar is the wire-format test, and it is
// the one that would have caught the defect this work item found in
// `internal/httpapi/auth`: `WriteUIPreferences` composes `theme=…; ui=…`, and a
// `;` inside a cookie value is the pair separator, so that value does not
// survive being written or read by anything.
//
// The assertion is end to end and on the exact bytes: a `http.Cookie` carrying
// the resolver's own format must arrive at the server as one cookie with both
// fields intact, and `http.SetCookie` must produce a `Set-Cookie` header that
// still holds both. Both directions are checked because the two ends fail
// differently — a value can be written and then not read back, and a value can
// be parsed and then never have been sent.
//
// What is *not* asserted here is that `auth.ReadUIPreferences` understands the
// format, because it does not: it splits on `;`. That is a defect in a file this
// work item does not own, it is reported with the one-line fix, and pinning it
// as an expectation here would mean a test that fails the moment somebody fixes
// it. `auth`'s two field *names* are checked in
// TestTheCookieFieldNamesAreTheOnesTheServerUses.
func TestTheCookieRoundTripsThroughACookieJar(t *testing.T) {
	t.Parallel()

	const written = "theme=dark&ui=tv"

	recorder := httptest.NewRecorder()
	http.SetCookie(recorder, &http.Cookie{
		Name:     auth.UICookieName,
		Value:    written,
		Path:     "/",
		MaxAge:   int(auth.UICookieMaxAge.Seconds()),
		SameSite: http.SameSiteLaxMode,
	})

	setCookie := recorder.Header().Get("Set-Cookie")
	if !strings.Contains(setCookie, written) {
		t.Errorf(
			"Set-Cookie %q lost part of the value %q; the resolver's cookie is "+
				"two fields in one cookie and needs a separator the cookie "+
				"grammar does not reserve", setCookie, written,
		)
	}

	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	request.AddCookie(&http.Cookie{Name: auth.UICookieName, Value: written})

	cookie, err := request.Cookie(auth.UICookieName)
	if err != nil {
		t.Fatalf("the server did not receive the cookie: %v", err)
	}

	if cookie.Value != written {
		t.Errorf(
			"the server read the preference as %q, want %q; a value that does not "+
				"survive the round trip means the resolver cannot read back what "+
				"it wrote", cookie.Value, written,
		)
	}
}

// TestTheCookieFieldNamesAreTheOnesTheServerUses holds the resolver's two field
// names to `auth`'s, because they are two halves of one wire format written in
// two languages, and a field name is the one half that cannot be discovered at
// runtime.
//
// The oracle is `auth.WriteUIPreferences` rather than `auth.ReadUIPreferences`,
// and the choice is itself the finding. `WriteUIPreferences` composes
// `theme=…; ui=…`, `;` is the cookie pair separator, and `http.Cookie.String()`
// drops it — it says so in the log line this test prints. The `Set-Cookie`
// header arrives as `sp_ui="theme=dark ui=tv"`, so feeding that back through
// `AddCookie` and `ReadUIPreferences` cannot return either field, in this
// build, whatever separator a caller uses: the value is already mangled before
// any parser sees it. The names still come through the write path intact, which
// is what makes them checkable. The separator is reported, not pinned.
func TestTheCookieFieldNamesAreTheOnesTheServerUses(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	auth.WriteUIPreferences(recorder, auth.UIPreferences{
		Theme: auth.ThemeDark,
		UI:    auth.UIModeTV,
	})

	setCookie := recorder.Header().Get("Set-Cookie")
	if setCookie == "" {
		t.Fatal("auth.WriteUIPreferences wrote no cookie")
	}

	for _, field := range []string{"theme", "ui"} {
		if !strings.Contains(setCookie, field+"=") {
			t.Errorf(
				"auth.WriteUIPreferences wrote %q, which does not carry the %q "+
					"field; the resolver names the same two fields, and the names "+
					"are the half of the format that cannot be discovered at "+
					"runtime", setCookie, field,
			)
		}
	}

	source := shell.ResolverSource()
	for _, fragment := range []string{`sp_ui=theme=`, `&ui=`} {
		if !strings.Contains(source, fragment) {
			t.Errorf(
				"the resolver does not write %q, so its cookie does not carry the "+
					"two field names the server writes", fragment,
			)
		}
	}

	if auth.UICookieName != "sp_ui" {
		t.Errorf("auth.UICookieName = %q, want sp_ui", auth.UICookieName)
	}
}

// TestTheResolverReadsThePreferenceInEveryShapeItArrivesIn checks the
// resolver's half of the wire format: that its tolerant split recovers both
// fields from the byte string it writes, and from the byte string this build's
// `auth.WriteUIPreferences` actually produces.
//
// The split is `/[;&\s]+/`, and the second case is why it is tolerant. `auth`
// composes `theme=dark; ui=tv`, the sanitiser removes the `;`, and what reaches
// the browser is `theme=dark ui=tv` — one space where the separator was. A
// resolver that split on its own separator alone would read the theme and lose
// the mode, which is a silent loss: the reader gets a laptop layout on a
// television and has nothing on screen that would explain it.
func TestTheResolverReadsThePreferenceInEveryShapeItArrivesIn(t *testing.T) {
	t.Parallel()

	// A mirror of the resolver's own split and `key=value` parse. It is a mirror
	// rather than the thing itself because the script runs in a browser; what is
	// asserted here is that the parse it performs recovers both fields from each
	// shape, which is the property that shape does not change.
	parse := func(raw string) map[string]string {
		fields := map[string]string{}

		seps := func(r rune) bool { return r == '&' || r == ';' || r == ' ' || r == '\t' }
		for _, pair := range strings.FieldsFunc(raw, seps) {
			key, value, found := strings.Cut(pair, "=")
			if !found {
				continue
			}

			fields[key] = value
		}

		return fields
	}

	// The mode is the one that must survive every shape. A lost theme is a wrong
	// colour scheme; a lost mode is a person standing in front of a television
	// reading a 12px wiki tree.
	for _, raw := range []string{
		"theme=dark&ui=tv",          // what the resolver writes
		"theme=dark ui=tv",          // what auth.WriteUIPreferences produces today
		"theme=auto;ui=laptop",      // the other separator, for an older build
		"theme=auto&ui=phone&ui=tv", // a repeated key: last wins, no panic
	} {
		if got := parse(raw)["ui"]; got == "" {
			t.Errorf(
				"parsing %q lost the ui field; a silently dropped mode is a "+
					"laptop layout on a television", raw,
			)
		}
	}

	// A value with no `=` contributes nothing rather than an empty field, which
	// is what keeps a truncated cookie from becoming `ui=""` and then a mode
	// this build does not know.
	if got := parse("theme=dark&ui"); got["ui"] != "" {
		t.Errorf("a field with no value parsed as ui=%q, want absent", got["ui"])
	}
}

// headDocument assembles a document the way a template is meant to: the
// colour-scheme meta, then the resolver, then the stylesheet link.
func headDocument(t *testing.T) string {
	t.Helper()

	var out strings.Builder

	out.WriteString("<!DOCTYPE html><html lang=\"en\"><head><meta charset=\"utf-8\">")

	if err := shell.ColorSchemeMeta().Render(t.Context(), &out); err != nil {
		t.Fatalf("render the colour-scheme meta: %v", err)
	}

	if err := shell.Resolver().Render(t.Context(), &out); err != nil {
		t.Fatalf("render resolver: %v", err)
	}

	out.WriteString(`<link rel="stylesheet" href="/assets/app.css">`)
	out.WriteString("</head><body></body></html>")

	return out.String()
}

// renderDocument renders a component to a string.
func renderDocument(t *testing.T, component templ.Component) (string, error) {
	t.Helper()

	var out strings.Builder
	if err := component.Render(t.Context(), &out); err != nil {
		return "", fmt.Errorf("render component: %w", err)
	}

	return out.String(), nil
}

// readScript reads the checked-in resolver, which lives outside this package.
func readScript(t *testing.T) string {
	t.Helper()

	return string(readFile(t, "..", "..", "web", "static", "js", "head.js"))
}

// readFile reads a file named by path segments, relative to this package.
//
// One helper rather than one per file, because the stylesheet and the resolver
// are reached the same way and a copy of the same four lines in two tests is
// two places to fix a path.
func readFile(t *testing.T, segments ...string) []byte {
	t.Helper()

	path := filepath.Join(segments...)

	// The path is assembled from fixed literals in this file and from nothing a
	// request or an environment supplied, and G304 is excluded repo-wide.
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return content
}

// compile-time proof that the head pieces are components and therefore reach a
// template as one call each. Without this, a change to a plain string would
// compile and every call site would need a Render of its own.
var (
	_ templ.Component = shell.Resolver()
	_ templ.Component = shell.ColorSchemeMeta()
)
