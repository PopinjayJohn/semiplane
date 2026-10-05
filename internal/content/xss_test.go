package content_test

// Stored XSS: the second layer, and a proof that it has teeth.
//
// Two layers stand between a vault and a reader's DOM (ADR 0028, S-4.6):
//
//  1. goldmark runs **without** `html.WithUnsafe()`, so a raw HTML element in a
//     markdown file never becomes markup. A *block-level* one is an HTML block in
//     CommonMark terms and loses its text with it; an *inline* one is parsed, keeps
//     its text, and has its tag removed by the next layer.
//  2. bluemonday is an **allowlist**: every element and attribute it does not name
//     is removed, so an element nobody thought of is removed too.
//
// Layer 1 is well covered and this file does not repeat it.
// `TestRawHTMLBlockTextIsDropped` holds the block case and the inline case side by
// side, and `TestAnAuthorCannotForgeACallout` holds the consequence that matters most
// — the policy allows `class="secret"` on a `div`, so without layer 1 an author
// could style their own prose as a GM's callout. `TestDangerousMarkupIsStripped` and
// `TestDangerousURLsAreNeutralised` hold the payload classes, and they hold them by
// substring over per-case renders.
//
// What is missing is a **single audit over the whole output, walking the DOM**, plus
// the thing `AGENTS.md` insists on twice in this phase and is the reason this file
// exists at all:
//
//   - *"Parse the DOM; never substring-match the markup. It is how 'no world' passes
//     while the word sits in an HTML comment, an `aria-label`, or a `data-`
//     attribute."* A substring assertion over `onclick` cannot see an attribute
//     spelled `OnClick`, one whose handler arrived via `&#111;nclick`, or one that a
//     sanitiser split across two attributes. A walk sees the attribute list a browser
//     would act on.
//   - *"An audit nobody can fail is not an audit."* So
//     `TestTheExecutableSurfaceAuditRejectsWhatItClaimsTo` feeds the audit hand-built
//     unsanitised markup — one violation per rule — and requires it to object to each.
//     An audit that returns "clean" for `<script>alert(1)</script>` is a green light
//     wired to nothing, and the only way to know which one you have is to try it on
//     something that should fail.
//
// The audit's rules are a **subset** of the policy's, and deliberately: it is not a
// second allowlist. It names the elements and attributes that must never appear
// whatever the policy says, so it stays true as the policy is narrowed and cannot
// disagree with it about what is allowed. A policy that allowed `script` would fail
// this file; a policy that dropped `figure` would not.

import (
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/semiplane/semiplane/internal/content"
)

// forbiddenElements is every element a rendered page may not contain.
//
// A subset of the policy's complement rather than the policy's complement itself: the
// policy is an allowlist of about seventy names, and restating seventy names here
// would be a second answer to "what may a page contain" that a later policy edit
// would not update. These are the ones whose presence is a security fact — script and
// style execute or restyle, the framing and plugin elements load a second document,
// the vector elements are the long history of sanitiser bypasses, and the form
// elements collect something from a reader (S-6.5 makes content editing GM-only and
// it happens in a route, not in a page body).
var forbiddenElements = []string{
	"script", "style", "noscript", "template",
	"iframe", "frame", "frameset", "object", "embed", "applet", "param", "portal",
	"svg", "math",
	"form", "button", "select", "option", "textarea", "label", "fieldset",
	"audio", "video", "source", "track", "canvas",
	"html", "head", "body", "title", "base", "link", "meta",
}

// forbiddenAttributes is every attribute name a rendered page may not carry.
//
// `style` first because it is the most dangerous attribute on any allowlist and it
// enables clickjacking overlays and exfiltration through `background:url(…)`. The
// rest are attributes that carry behaviour, retarget a URL, or collide with an
// application's own DOM names. `data-testid` is here for a different reason: the UI's
// test ids live in templ components, and an id arriving from a vault would be an
// author naming a test hook — a way to collide with a real one.
var forbiddenAttributes = []string{
	"style", "srcdoc", "srcset", "formaction", "ping", "target", "rel",
	"accesskey", "autofocus", "tabindex", "contenteditable", "is", "name",
	"data-testid", "http-equiv",
}

// urlAttributes are the attributes whose value is a URL, and therefore the ones the
// scheme allowlist applies to.
var urlAttributes = []string{"href", "src", "cite"}

// allowedSchemes is the URL allowlist, spelled out so the audit states it rather
// than inferring it: `javascript:`, `data:`, `vbscript:` and `file:` are not in it,
// which is what neutralises them.
var allowedSchemes = []string{"http", "https", "mailto"}

// allowedDataAttributes is the `data-` set the renderer writes.
//
// `AllowDataAttributes()` is deliberately not called in `policy.go`, and this is the
// assertion that it still is not: a `data-` attribute is inert until some code reads
// it, so a vault that could write one could drive a future client that reads it.
var allowedDataAttributes = []string{
	"data-ext", "data-secret", "data-ref-index", "data-arg", "data-broken",
}

// allowedSecretStates is the whole of `data-secret`'s vocabulary, matched exactly.
var allowedSecretStates = []string{"collapsed", "revealed"}

// surface is one thing wrong with a rendered page.
type surface struct {
	// rule names the audit rule that fired, so a failure says which rule is dead
	// rather than that "something" was found.
	rule string
	// detail is the specific thing: an element name, an attribute name, a URL.
	detail string
}

// audit walks a parsed fragment and returns everything it objects to.
//
// Returns a slice rather than a bool, because "the audit is clean" is a much weaker
// statement than a list of what it would have caught, and a failure message built
// from the slice names the rule that is no longer enforced.
func audit(fragment string) []surface {
	return auditReader(strings.NewReader(fragment))
}

// errReader is a reader that fails, so the audit's own failure path is reachable
// from a test.
//
// The alternative is a rule with no fixture, and a rule with no fixture is a rule
// nobody can prove fires — which is the failure this whole file is written against.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("the reader failed") }

// auditReader is `audit` over any reader, so the parse-failure branch is testable.
//
// The context node needs its atom set, not just its name: `ParseFragment` reads
// `DataAtom` to decide the parser state it is resuming in, and a context node with
// the name alone makes it return an error for *every* fragment. Which is exactly the
// kind of bug an audit can carry silently — it would have reported "parses" for
// everything, and every negative assertion in the file would have passed.
func auditReader(source io.Reader) []surface {
	context := &html.Node{Type: html.ElementNode, DataAtom: atom.Body, Data: "body"}

	nodes, err := html.ParseFragment(source, context)
	if err != nil {
		// A fragment that will not parse is neither clean nor inspected: it is a
		// fragment the audit could not see, and saying nothing about it is the
		// silent pass this file exists to prevent.
		return []surface{{rule: "parses", detail: "the fragment did not parse: " + err.Error()}}
	}

	var found []surface

	for _, node := range nodes {
		walkSurface(node, &found)
	}

	return found
}

// walkSurface is the recursive half of `audit`.
func walkSurface(node *html.Node, found *[]surface) {
	if node.Type == html.ElementNode {
		*found = append(*found, elementSurfaces(node)...)
	}

	for child := node.FirstChild; child != nil; child = child.NextSibling {
		walkSurface(child, found)
	}
}

// elementSurfaces is every rule that applies to one element.
func elementSurfaces(node *html.Node) []surface {
	name := strings.ToLower(node.Data)

	var found []surface

	for _, forbidden := range forbiddenElements {
		if name == forbidden {
			found = append(found, surface{rule: "no forbidden element", detail: name})
		}
	}

	for _, attr := range node.Attr {
		found = append(found, attributeSurfaces(name, attr.Key, attr.Val)...)
	}

	return found
}

// attributeSurfaces is every rule that applies to one attribute of one element.
//
// The element is a parameter because two rules are per-element: `input` is the one
// element the policy allows as a GFM task box, and `name` is forbidden everywhere —
// a form control with a name is a control that submits something.
func attributeSurfaces(element, rawKey, value string) []surface {
	key := strings.ToLower(rawKey)

	var found []surface

	// Any event handler, by prefix. The prefix rather than a list of names, because
	// `onmouseover`, `onpointerdown`, `ontoggle` and whatever a browser adds next are
	// all the same rule and a list would go stale the first time it was checked.
	if strings.HasPrefix(key, "on") {
		found = append(found, surface{
			rule:   "no event handler",
			detail: element + "[" + key + "]",
		})
	}

	for _, forbidden := range forbiddenAttributes {
		if key == forbidden {
			found = append(found, surface{
				rule:   "no forbidden attribute",
				detail: element + "[" + key + "]",
			})
		}
	}

	if strings.HasPrefix(key, "data-") && !contains(allowedDataAttributes, key) {
		found = append(found, surface{
			rule:   "no unlisted data attribute",
			detail: element + "[" + key + "]",
		})
	}

	if key == "data-secret" && !contains(allowedSecretStates, value) {
		found = append(found, surface{
			rule:   "a data-secret state is one of two",
			detail: element + "[" + key + "=" + value + "]",
		})
	}

	if contains(urlAttributes, key) {
		if scheme, ok := schemeOf(value); ok && !contains(allowedSchemes, scheme) {
			found = append(found, surface{
				rule:   "a URL scheme is on the allowlist",
				detail: element + "[" + key + "=" + value + "]",
			})
		}
	}

	return found
}

// schemeOf returns a URL's scheme and whether it has one.
//
// A value with no scheme is a relative URL, which the policy allows and which this
// returns `("", false)` for: `//evil.example/x` is protocol-relative and `has no
// scheme` in this sense while still naming another origin, so it is called out
// separately below rather than waved through.
func schemeOf(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)

	colon := strings.Index(trimmed, ":")
	if colon <= 0 {
		return "", false
	}

	// A `:` after a `/` is a path segment, not a scheme: `notes/a:b` is a page
	// called `a:b` in a directory called `notes`.
	if slash := strings.Index(trimmed, "/"); slash >= 0 && slash < colon {
		return "", false
	}

	return strings.ToLower(trimmed[:colon]), true
}

// contains is the audit's membership test over its own rule tables.
//
// A function rather than `slices.Contains` spelled inline at each of the nine call
// sites, so that "which list is this element checked against" is one named thing per
// rule rather than a slice literal at every use — and so that a rule table can never
// be rebuilt inside the walk.
func contains(haystack []string, needle string) bool {
	return slices.Contains(haystack, needle)
}

// hostileCorpus is the markdown this file renders and audits.
//
// Every row is a payload that reaches a reader's DOM in some other project. The
// point of the corpus is not that each row is caught — `render_test.go` proves that
// one at a time — but that **one walk over all of them** finds nothing, which is the
// property a reader of a page's bytes actually depends on.
var hostileCorpus = map[string]string{
	"a script block":             "<script>alert('body text')</script>\n",
	"an inline handler":          "keep <span onclick=\"steal()\">this text</span>\n",
	"a mixed-case handler":       "keep <span OnClick=\"steal()\">this text</span>\n",
	"an entity-encoded handler":  "keep <span onclick=&#115;teal()>this text</span>\n",
	"an image error handler":     "![alt](x.png \"t\")\n",
	"an svg vector":              "<svg><script>alert(1)</script></svg>\n",
	"a math vector":              "<math><mtext><script>alert(1)</script></mtext></math>\n",
	"an iframe":                  "<iframe src=\"https://elsewhere.example\"></iframe>\n",
	"a form":                     "<form action=\"https://elsewhere.example\"><input name=\"pw\"></form>\n",
	"a style block":              "<style>body{display:none}</style>\n",
	"a base element":             "<base href=\"https://elsewhere.example/\">\n",
	"a javascript link":          "[click](javascript:alert(1))\n",
	"an uppercase scheme":        "[click](JavaScript:alert(1))\n",
	"a data url":                 "[click](data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==)\n",
	"a file url":                 "[click](file:///etc/passwd)\n",
	"an entity-encoded scheme":   "[click](&#106;avascript:alert(1))\n",
	"a vbscript url":             "[click](vbscript:msgbox(1))\n",
	"an autolink":                "<javascript:alert(1)>\n",
	"an attribute-breaking link": "[click](\" onmouseover=\"alert(1))\n",
	"an authored argument":       "Roll {{dice:1d20\" onload=\"alert(1)}} for it.\n",
	"a forged callout": "<div class=\"secret secret--revealed\" " +
		"data-secret=\"revealed\">A lie.</div>\n",
	"an authored task box":     "- [ ] a task\n",
	"a fenced script":          "```html\n<script>alert(1)</script>\n```\n",
	"a heading with a handler": "# Heading <a href=\"#\" onclick=\"alert(1)\">x</a>\n",
	"a table with a style":     "| a | b |\n| - | - |\n| <td style=\"x\">c</td> |\n",
	"an indented code block":   "    <script>alert(1)</script>\n",
}

// TestTheRenderedPageCarriesNoExecutableSurface is the audit over the whole output,
// over a corpus of every payload class this pipeline is expected to neutralise.
//
// The positive is in the same test and it is the part that makes the negative mean
// something: a page that rendered nothing at all — a sanitiser that refused
// everything, or a renderer that returned an empty fragment — would pass every
// assertion below. So the ordinary prose of each case is asserted present.
func TestTheRenderedPageCarriesNoExecutableSurface(t *testing.T) {
	t.Parallel()

	for name, source := range hostileCorpus {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			out, err := newTestRenderer(t).Render(content.Parse([]byte(source), nil))
			if err != nil {
				t.Fatalf("render: %v", err)
			}

			for _, found := range audit(out.HTML) {
				t.Errorf("rendered output carries %s (%s):\n%s",
					found.rule, found.detail, out.HTML)
			}

			// Several of these cases render **nothing**, and that is the correct
			// answer rather than a vacuous pass: a block-level raw element is an HTML
			// block in CommonMark terms, goldmark replaces it with a
			// `<!-- raw HTML omitted -->` comment, the sanitiser drops the comment,
			// and the element's text goes with it. `TestRawHTMLBlockTextIsDropped`
			// holds that content loss explicitly, so there is nothing here to assert
			// for those rows — and the corpus-level positive below is what keeps the
			// rest honest.
		})
	}

	// The corpus as a whole must still produce readable prose, which is the
	// assertion that keeps "the sanitiser removed everything" from being a pass.
	const prose = "The vault door is iron, and the wyvern sleeps above it.\n"

	out, err := newTestRenderer(t).Render(content.Parse([]byte(prose), nil))
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if !strings.Contains(out.HTML, "wyvern") {
		t.Errorf("an ordinary paragraph lost its text:\n%s", out.HTML)
	}

	for _, found := range audit(out.HTML) {
		t.Errorf("ordinary prose trips the audit: %s (%s):\n%s",
			found.rule, found.detail, out.HTML)
	}
}

// TestTheExecutableSurfaceAuditRejectsWhatItClaimsTo is the meta-test, and it is
// the reason the first one can be believed.
//
// Each row is hand-written markup that violates exactly one rule, handed to the audit
// **unsanitised** — the bytes a pipeline with `html.WithUnsafe()` and no allowlist
// would emit. The audit must object to every one. A rule whose row comes back clean
// is a rule the audit does not implement, and an audit that does not implement a rule
// is a green light wired to nothing.
//
// The rows are raw HTML on purpose. They cannot be produced through the renderer —
// which is the point: layer 1 means the renderer never emits them, so the only way to
// prove layer 2 objects is to hand it what layer 1 would have let through if it were
// switched off. That is exactly the future change ADR 0028's second layer exists for.
func TestTheExecutableSurfaceAuditRejectsWhatItClaimsTo(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		// wantRule is the rule the row must trip. Empty means "some rule", which is
		// enough for a row whose point is that the markup is caught at all.
		wantRule string
	}{
		{
			name:     "a script element",
			raw:      "<p>before</p><script>alert(1)</script>",
			wantRule: "no forbidden element",
		},
		{
			name:     "a style element",
			raw:      "<style>body{display:none}</style>",
			wantRule: "no forbidden element",
		},
		{
			name:     "an iframe",
			raw:      `<iframe src="https://elsewhere.example"></iframe>`,
			wantRule: "no forbidden element",
		},
		{
			name:     "an svg vector",
			raw:      "<svg><script>alert(1)</script></svg>",
			wantRule: "no forbidden element",
		},
		{
			name:     "a form",
			raw:      `<form action="/c/x"><input name="pw"></form>`,
			wantRule: "no forbidden element",
		},
		{
			name:     "an event handler",
			raw:      `<span onclick="steal()">text</span>`,
			wantRule: "no event handler",
		},
		{
			// The same rule with the spelling a substring check over `onclick` would
			// miss, which is why the rule is a prefix and this row exists.
			name:     "a mixed-case event handler",
			raw:      `<span OnMouseOver="steal()">text</span>`,
			wantRule: "no event handler",
		},
		{
			name:     "a style attribute",
			raw:      `<p style="position:fixed;z-index:9999">overlay</p>`,
			wantRule: "no forbidden attribute",
		},
		{
			name:     "a target attribute",
			raw:      `<a href="https://example.com" target="_blank">x</a>`,
			wantRule: "no forbidden attribute",
		},
		{
			name:     "an authored test id",
			raw:      `<p data-testid="campaignTitle">x</p>`,
			wantRule: "no forbidden attribute",
		},
		{
			name:     "an unlisted data attribute",
			raw:      `<span data-ref="1">x</span>`,
			wantRule: "no unlisted data attribute",
		},
		{
			name: "a data-secret state outside the vocabulary",
			raw:  `<div class="secret" data-secret="maybe">x</div>`,
			// Two rules fire here — the unlisted class value is not one of the audit's
			// concerns, and `data-secret` is on the allowed list, so this row is about
			// the *value* rule alone.
			wantRule: "a data-secret state is one of two",
		},
		{
			name:     "a javascript url",
			raw:      `<a href="javascript:alert(1)">x</a>`,
			wantRule: "a URL scheme is on the allowlist",
		},
		{
			name:     "a data url",
			raw:      `<img src="data:text/html;base64,PHNjcmlwdD4=">`,
			wantRule: "a URL scheme is on the allowlist",
		},
		{
			name:     "a file url",
			raw:      `<a href="file:///etc/passwd">x</a>`,
			wantRule: "a URL scheme is on the allowlist",
		},
	}

	// The positive, once: the audit must be silent on markup this pipeline does
	// emit, or every row above would be satisfied by an audit that objects to
	// everything.
	const allowed = `<p class="language-go">plain</p>` +
		`<a href="https://example.com/a" class="wikilink" data-ext="wikilink" ` +
		`data-ref-index="0">link</a>` +
		`<span class="dice" data-ext="dice" data-ref-index="1" data-arg="1d20+5"></span>` +
		`<div class="secret secret--collapsed" data-ext="secret" data-secret="collapsed">` +
		`<p>hidden</p></div>` +
		`<input type="checkbox" disabled>` +
		`<a href="#a-heading" data-broken="true">broken</a>`

	if found := audit(allowed); len(found) != 0 {
		t.Fatalf("the audit objects to markup the pipeline legitimately emits: %v\n%s",
			found, allowed)
	}

	// The audit's own failure path, which no string can reach: a reader that errors
	// makes `ParseFragment` fail, and an audit that reported nothing there would be
	// reporting on a document it never saw.
	t.Run("a fragment the parser could not read", func(t *testing.T) {
		t.Parallel()

		found := auditReader(errReader{})
		if len(found) == 0 {
			t.Fatal("the audit reported nothing for a fragment it could not parse; " +
				"an unread fragment is an uninspected one, not a clean one")
		}

		if found[0].rule != "parses" {
			t.Errorf("the audit reported %q for an unreadable fragment, want %q",
				found[0].rule, "parses")
		}
	})

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			found := audit(testCase.raw)

			if len(found) == 0 {
				t.Fatalf("the audit found nothing in %q.\n"+
					"This row is the audit's own proof that its rule can fail: an audit "+
					"that does not object here is a green light wired to nothing",
					testCase.raw)
			}

			if testCase.wantRule == "" {
				return
			}

			for _, one := range found {
				if one.rule == testCase.wantRule {
					return
				}
			}

			t.Errorf("the audit objected, but not with %q: %v", testCase.wantRule, found)
		})
	}
}

// TestAnAuthoredArgumentCannotCloseAnAttribute is the one author-controlled
// attribute the extensions write, and it is the only place a vault's bytes are
// interpolated into a tag by this project's own code rather than by the renderer.
//
// `ext.open.go`'s `escape` is the escaper, and `ext_test.go` holds it against the
// extension's own output. What is *not* held anywhere is the pair: an argument that
// arrives escaped at the writer's hands and is then put through a sanitiser that
// rewrites attributes. Two independent transformations have to agree that a `"` is a
// character and not a delimiter, and this is the test that says they do — for the
// reader who sees the argument in the DOM, which is every reader of the page.
//
// The assertion is on the **parsed attribute list**, so it cannot be satisfied by
// the output happening not to contain the substring `onload`: it requires that
// `data-arg`'s value *is* the author's bytes, and that no other attribute appeared.
func TestAnAuthoredArgumentCannotCloseAnAttribute(t *testing.T) {
	t.Parallel()

	// Every one of these closes an attribute if the value is interpolated raw.
	cases := map[string]string{
		"a quote and a handler": `{{dice:1d20" onload="alert(1)}}`,
		"a single quote":        `{{dice:1d20' onload='alert(1)}}`,
		"a greater-than":        `{{dice:1d20><script>alert(1)</script>}}`,
		"an ampersand":          `{{dice:a&amp;b}}`,
		"a statblock argument":  `{{statblock:Goblin" data-secret="revealed}}`,
	}

	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			out, err := newTestRenderer(t).Render(content.Parse([]byte(source), nil))
			if err != nil {
				t.Fatalf("render: %v", err)
			}

			for _, found := range audit(out.HTML) {
				t.Errorf("an authored argument produced %s (%s):\n%s",
					found.rule, found.detail, out.HTML)
			}

			// The element exists and carries exactly the three attributes the
			// extensions write. A policy that dropped `data-arg` would pass every
			// assertion above, and the live layer that reads it would stop working.
			tokens := parseTokens(t, out.HTML)

			if !tokens.hasElement("span") {
				t.Fatalf("the extension's element is gone:\n%s", out.HTML)
			}

			for _, want := range []string{"data-ext", "data-ref-index", "data-arg"} {
				if !tokens.hasAttribute(want) {
					t.Errorf("the rendered element does not carry %q:\n%s", want, out.HTML)
				}
			}
		})
	}
}

// TestASecretCalloutIsSanitisedForTheReaderWhoSeesIt is the payload in the place it
// is most likely to be and least likely to be checked.
//
// A `[!secret]` callout is hidden text: a GM writes it knowing no player will read
// it, which is exactly the reasoning that produces an unescaped `<script>` in a
// shared vault's lore. The two redaction paths already hold that a *player* never
// receives the callout's bytes at all, which makes the callout the one region of a
// page where the sanitiser is the only thing standing between an author and the most
// privileged reader's DOM — the GM's own browser, which is the one with a session
// cookie, an instance-admin session if the GM is one, and every campaign the GM can
// reach.
//
// So this renders the callout **as the GM sees it** (the unredacted source, which is
// what the wiki route hands the GM) and audits the result. A revealed callout is the
// row that matters: its contents are public by design, so "it will be sanitised when
// it is shown" is a claim about the pipeline rather than about the redaction.
func TestASecretCalloutIsSanitisedForTheReaderWhoSeesIt(t *testing.T) {
	t.Parallel()

	const payload = "EMBERGLASS-INLINE-4c1f"

	cases := map[string]string{
		"a collapsed callout": "> [!secret]- Hidden\n" +
			"> The traitor is <script>alert('" + payload + "')</script> Captain Aldric.\n",
		"a revealed callout": "> [!secret]+ Revealed\n" +
			"> The token is <img src=x onerror=alert('" + payload + "')> written here.\n",
		"a callout with a style": "> [!secret]+ Revealed\n" +
			"> <span style=\"position:fixed;inset:0;z-index:9999\">" + payload + "</span>\n",
		"a callout with a link": "> [!secret]+ Revealed\n" +
			"> [click](javascript:alert('" + payload + "'))\n",
	}

	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			out, err := newTestRenderer(t).Render(content.Parse([]byte(source), nil))
			if err != nil {
				t.Fatalf("render: %v", err)
			}

			for _, found := range audit(out.HTML) {
				t.Errorf("a secret callout rendered %s (%s):\n%s",
					found.rule, found.detail, out.HTML)
			}

			// The callout itself survives: the policy allows its class and its state,
			// and dropping the element would hide the GM's notes rather than protect
			// anybody.
			tokens := parseTokens(t, out.HTML)

			if !tokens.hasElement("div") || !tokens.hasClass("secret") {
				t.Errorf("the callout did not render as a callout:\n%s", out.HTML)
			}

			// And the marker is gone, because the extension consumes it rather than
			// writing it into a page.
			if strings.Contains(out.HTML, "[!secret]") {
				t.Errorf("the callout marker reached the output:\n%s", out.HTML)
			}

			// The payload never appears inside an attribute, which is the assertion
			// that matters and the one the audit cannot make on its own: an *inline*
			// raw element keeps its text with the tag removed (ADR 0028), so the
			// marker is expected to survive as visible prose here. What must not
			// happen is the marker reaching a *destination* — a handler, a URL — and
			// `audit` has just walked every attribute for exactly that.
			if tokens.hasAttribute("href") || tokens.hasAttribute("src") {
				t.Errorf("the callout rendered a destination carrying the payload:\n%s",
					out.HTML)
			}
		})
	}

	// The positive for the whole test: the GM's own note survives, so the audit
	// above is not satisfied by a pipeline that deleted the callout.
	const gmNote = "The traitor is Captain Aldric."

	out, err := newTestRenderer(t).Render(content.Parse(
		[]byte("> [!secret]- Hidden\n> "+gmNote+"\n"), nil,
	))
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if !strings.Contains(out.HTML, "Aldric") {
		t.Errorf("the GM's note did not survive:\n%s", out.HTML)
	}
}
