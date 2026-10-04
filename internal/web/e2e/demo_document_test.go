package e2e_test

// One fetched page, and the walks over it.
//
// # Split from the fixture on purpose
//
// `demo_harness_test.go` is about **getting** a running instance of the shipped
// server; this file is about **reading what it serves**. The boundary is where the
// subject changes: everything above builds a process, everything below parses a
// response.
//
// # Parsed, never substring-matched, and the two are not interchangeable
//
// Every claim this package makes about a document is a claim about a *tree*. The
// distinction is not fastidiousness -- it is the difference between a check that can
// fail and one that cannot:
//
//   - `play_test.go` records that this phase has already produced four checks reading
//     raw text and reporting a violation where there was none, because a `<script`
//     inside a comment is prose;
//   - a substring search over the response for a secret's text finds it in an HTML
//     comment, and says nothing about whether a reader could see it; and
//   - a substring search for a heading matches it inside an attribute value, and
//     reports a page's body as missing when the body is present.
//
// `golang.org/x/net/html` does not believe any of those, which is the property
// `document_test.go`'s header already argues for and this file extends.
//
// # The two walks, and which claim each answers
//
//   - `demoText` concatenates **text nodes** only. It answers "does the reader read
//     this?", so it cannot answer "is this in the response?" and is not asked to.
//   - `demoCarry` walks text nodes, **every attribute value** and **every comment**. It
//     answers "can this response be made to show it?", which is the question §5.6.1 is
//     about and the one `curl` asks.
//
// Asking either walk to do the other's job produces a test that passes for the wrong
// reason, and that is the failure this file's split exists to make hard to write.

import (
	"bytes"
	"io"
	"maps"
	"net/http"
	neturl "net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// demoDocument is one fetched page: its bytes and its parsed tree.
//
// **The status is not kept**, because every caller has already required a 200 before
// it gets here — and a struct carrying a status nobody reads is a field that will be
// read by the next caller that forgets.
type demoDocument struct {
	body []byte
	root *html.Node
}

// requireDocument fetches a page and fails unless it is a 200 HTML document.
//
// **Failing rather than parsing whatever arrived**, for `parsePlayDocument`'s
// reason: the claims below are about a document, and a response that is not one
// means the route answered something else — a refusal JSON body, an empty 404 —
// and every assertion about it would be about that.
func (b *demoBoot) requireDocument(t *testing.T, client *http.Client, url string) demoDocument {
	t.Helper()

	status, body := demoGet(t, client, url)

	if status != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200.\nbody:\n%s", url, status, truncate(string(body), 1500))
	}

	return demoParse(t, body, url)
}

// demoParse turns bytes that have already been checked into a parsed document.
//
// **Split out from `requireDocument` because a POST's response is the document under
// test just as much as a GET's is.** The dice roller answers a roll on the POST, and
// re-fetching the page to look at the answer would read a document with no outcome on
// it — a different document, answering a different question.
func demoParse(t *testing.T, body []byte, url string) demoDocument {
	t.Helper()

	root, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("parse the document at %s: %v", url, err)
	}

	return demoDocument{body: body, root: root}
}

// demoGet issues one GET and returns the status and the whole body.
//
// **Both, always.** A helper that returned a `*http.Response` would hand out a
// body nobody closes, which `bodyclose` catches here and which against a real
// server is a connection returned to the pool late rather than never.
func demoGet(t *testing.T, client *http.Client, url string) (int, []byte) {
	t.Helper()

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatalf("build the request for %s: %v", url, err)
	}

	return doDemoRequest(t, client, request, url)
}

// demoValidator fetches a URL and returns the `ETag` the server sent.
//
// **A separate helper rather than a header out of `demoGet`,** because the only
// claim in this suite that reads a header is ADR 0016's — a GM's validator and a
// player's must not be the same — and threading a `http.Header` through every
// response to serve one assertion would put a `nil`-check on every call site.
func demoValidator(t *testing.T, client *http.Client, url string) string {
	t.Helper()

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatalf("build the request for %s: %v", url, err)
	}

	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer response.Body.Close()

	if _, drainErr := io.Copy(io.Discard, response.Body); drainErr != nil {
		t.Fatalf("read the response to %s: %v", url, drainErr)
	}

	validator := response.Header.Get("ETag")
	if validator == "" {
		t.Fatalf("GET %s sent no ETag. The wiki route derives it from the cache key, "+
			"and a page served with no validator is a page a shared cache may store "+
			"and hand to the wrong reader", url)
	}

	return validator
}

// demoSessionCookie is the reader's own `sp_session`, for a request a cookie jar
// will not be attached to.
//
// **A WebSocket handshake needs this and it is worth naming why.**
// `coder/websocket`'s `Dial` is not an `http.Client` with a jar: it takes a
// `DialOptions` and sends the headers it is given, so a dial that omitted this would
// arrive **anonymous**. On a private campaign that is a 404, which is the correct
// answer and would have looked here like a broken socket.
//
// Reading the cookie out of the jar rather than threading it through `signIn` keeps
// one source of truth: the jar is where the session is, because that is where a
// browser holds it.
func demoSessionCookie(t *testing.T, client *http.Client, base string) string {
	t.Helper()

	parsed, err := neturl.Parse(base + "/")
	if err != nil {
		t.Fatalf("parse the instance base URL %q: %v", base, err)
	}

	for _, cookie := range client.Jar.Cookies(parsed) {
		if cookie.Name == "sp_session" && cookie.Value != "" {
			return cookie.Value
		}
	}

	t.Fatalf("this reader holds no sp_session cookie, so a request made on its behalf " +
		"would be anonymous. `signIn` requires one, so this means the reader was never " +
		"signed in")
	return ""
}

// demoHeaders fetches a URL and returns the response headers.
//
// **A helper of its own rather than a header threaded through `demoGet`**, for
// `demoValidator`'s reason: exactly two claims in this suite read a header -- ADR
// 0016's validator, and `Cache-Control: private, no-store` on a refusal -- and
// putting a `http.Header` on every call site to serve them would add a nil-check to
// every caller.
func demoHeaders(t *testing.T, client *http.Client, url string) http.Header {
	t.Helper()

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatalf("build the request for %s: %v", url, err)
	}

	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer response.Body.Close()

	if _, drainErr := io.Copy(io.Discard, response.Body); drainErr != nil {
		t.Fatalf("read the response to %s: %v", url, drainErr)
	}

	return response.Header.Clone()
}

// demoPostForm issues one form POST and returns the status and the whole body.
func demoPostForm(
	t *testing.T,
	client *http.Client,
	url string,
	fields neturl.Values,
) (int, []byte) {
	t.Helper()

	request, err := http.NewRequestWithContext(
		t.Context(), http.MethodPost, url, strings.NewReader(fields.Encode()),
	)
	if err != nil {
		t.Fatalf("build the form request for %s: %v", url, err)
	}

	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	return doDemoRequest(t, client, request, url)
}

// doDemoRequest is the one place a demo-suite request is issued and drained.
func doDemoRequest(
	t *testing.T,
	client *http.Client,
	request *http.Request,
	url string,
) (int, []byte) {
	t.Helper()

	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", request.Method, url, err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read the response to %s %s: %v", request.Method, url, err)
	}

	return response.StatusCode, body
}

// truncate shortens a body for a failure message.
//
// A page is tens of kilobytes and a failure message is read on a terminal, so the
// alternative is a message whose useful part is forty screens down. The head is
// kept rather than the tail because the head is where a heading and the first
// paragraph are.
func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}

	return text[:limit] + "\n… (" + strconv.Itoa(len(text)-limit) + " more bytes)"
}

// demoText is every text node in a subtree, concatenated in document order.
//
// Over the **parsed tree**, never the raw bytes, and the distinction is this
// suite's method: "the secret's text is not in the response" is a claim about
// every byte including attributes and comments, which `demoText` alone cannot
// answer, and "the page's body rendered" is a claim about text nodes, which a
// substring search over raw HTML answers wrongly whenever the same text is also in
// an attribute. Each claim uses the walk that answers it.
func demoText(node *html.Node) string {
	var out strings.Builder

	var walk func(*html.Node)

	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			out.WriteString(current.Data)
		}

		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(node)

	return out.String()
}

// demoByTestID finds the element carrying a `data-testid`.
//
// **`data-testid` and not a class or a tag**, which is `AGENTS.md`'s rule and the
// reason it is a rule: a class is a styling decision a stylesheet owns, and a
// rename would silently un-hold an assertion.
func demoByTestID(root *html.Node, id string) *html.Node {
	var found *html.Node

	eachElement(root, func(node *html.Node) {
		if found == nil && attrOf(node, "data-testid") == id {
			found = node
		}
	})

	return found
}

// demoRequireTestID is `demoByTestID` with a failure, for an element a claim
// cannot proceed without.
//
// Failing rather than returning nil and letting the next assertion report a nil
// dereference: "the roll dialog is not on the page" and "the roll dialog's empty
// state is missing" are different facts, and only the first of them is true when
// the element is absent.
func demoRequireTestID(t *testing.T, doc demoDocument, id string) *html.Node {
	t.Helper()

	node := demoByTestID(doc.root, id)
	if node == nil {
		t.Fatalf("the document carries no element with data-testid=%q.\nbody:\n%s",
			id, truncate(string(doc.body), 1500))
	}

	return node
}

// demoElementsWithAttr returns every element whose attribute is exactly value.
//
// Exact rather than `Contains`, for the reason `AGENTS.md` records about
// `hasRule`: a substring test answers `true` for a document carrying only a
// modifier of what was wanted.
func demoElementsWithAttr(root *html.Node, name, value string) []*html.Node {
	var found []*html.Node

	eachElement(root, func(node *html.Node) {
		if attrOf(node, name) == value {
			found = append(found, node)
		}
	})

	return found
}

// demoCarryingClassToken returns every element with this token in its `class`.
//
// Token-wise and not as a substring, because `class="secret secret--revealed"`
// carries `secret--revealed` and `class="secret--revealed"` also carries it, while
// a substring test over the joined value would answer `true` for a document whose
// only `secret`-shaped class is `secret--revealed` when what was asked about was
// `secret`.
func demoCarryingClassToken(root *html.Node, token string) []*html.Node {
	var found []*html.Node

	eachElement(root, func(node *html.Node) {
		for candidate := range strings.FieldsSeq(attrOf(node, "class")) {
			if candidate == token {
				found = append(found, node)

				break
			}
		}
	})

	return found
}

// demoCarry reports everywhere a needle survives in a parsed document.
//
// **Every place a browser could be made to show it**, and the enumeration is the
// point rather than the tidiness:
//
//   - a text node — what a reader reads;
//   - every attribute value — what a `title`, an `alt`, a `data-` attribute or an
//     inline `style` would carry;
//   - a comment — a secret in a comment is in the response, and `curl` shows it to
//     whoever asks.
//
// A walk that read only text nodes would pass a document that put the secret in an
// attribute, which is exactly what §5.6.1 rules out and exactly what a player reads
// the source for.
//
// **Both sides are folded through `demoFold` first**, because goldmark's
// typographer rewrites the author's bytes: `greyhaven/rules/lantern-folk.md` writes
// `shed's` and the served document reads `shed’s`, so a raw search for the source
// line finds nothing in a page that carries it in full. That was measured, not
// guessed — the first run of this file failed on that page and on no other.
func demoCarry(node *html.Node, needle string) []string {
	wanted := demoFold(needle)

	var found []string

	var walk func(*html.Node)

	walk = func(current *html.Node) {
		switch current.Type {
		case html.TextNode, html.CommentNode:
			if strings.Contains(demoFold(current.Data), wanted) {
				found = append(found, "a "+nodeKindName(current.Type)+" node")
			}
		case html.ElementNode:
			for _, attribute := range current.Attr {
				if strings.Contains(demoFold(attribute.Val), wanted) {
					found = append(found, "<"+current.Data+" "+attribute.Key+"=…>")
				}
			}
		case html.DoctypeNode:
		default:
		}

		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(node)

	return found
}

// demoFold normalises the differences between what an author wrote and what the
// renderer emits.
//
// **Two rewritings, and both are the renderer's.** goldmark's typographer turns
// `'` into `\u2019`, `"` into `\u201c`/`\u201d`, `--`/`---` into `\u2013`/`\u2014` and `...` into
// `\u2026`; and a code span loses its backticks to a `<code>` element. Folding is
// applied to the needle and to the searched text alike, so a search still fails when
// the text is genuinely absent and succeeds when the only difference is a character
// the pipeline rewrote.
//
// Both were measured, not guessed. The typographer's apostrophes are why
// `greyhaven/rules/lantern-folk.md` failed this suite's first run; the backticks are
// why `forgotten-realm/the-system-it-asked-for.md` failed its first — its first
// heading is "What `system:` is, and where it is not".
//
// **What this does not weaken.** A needle that matched in one spelling and not the
// other is a false *negative* — a leak reported where there is none — and never a
// false positive, because the only way the fold helps is by making the two spellings
// equal. The mutation proofs below remove redaction outright, at which point the text
// is present in every spelling.
func demoFold(text string) string {
	return demoRewrites.Replace(text)
}

// demoRewrites is the fold, as a replacer so it is applied once per call rather than
// eight times.
var demoRewrites = strings.NewReplacer(
	"‘", "'", // ' left single quotation mark
	"’", "'", // ' right single quotation mark
	"“", `"`, // " left double quotation mark
	"”", `"`, // " right double quotation mark
	"–", "-", // – en dash
	"—", "-", // — em dash
	"…", "...", // … horizontal ellipsis
	"`", "", // ` a code span becomes an element and loses its delimiters
)

// nodeKindName names a node type for a failure message.
func nodeKindName(kind html.NodeType) string {
	switch kind {
	case html.TextNode:
		return "text"
	case html.CommentNode:
		return "comment"
	case html.DoctypeNode:
		return "doctype"
	case html.ElementNode:
		return "element"
	case html.DocumentNode:
		return "document"
	default:
		return "other"
	}
}

// demoNames is a set's members in a stable order, for a failure message that does
// not reorder between runs.
//
// Determinism is this repository's rule, and a *failure message* is where it is
// most often broken: a `map` range inside an error string makes a flaky test look
// like a flaky product.
func demoNames(set map[string]bool) []string {
	return slices.Sorted(maps.Keys(set))
}

// demoSubtracted is `want` minus `got`, both sorted.
//
// A set difference and not a loop over one of the sets, because the claim these
// support is "every kind this build registers has a page", and the failure message
// has to name the *missing* ones — which is the set nothing else produces.
func demoSubtracted(want, got map[string]bool) []string {
	missing := map[string]bool{}

	for name := range want {
		if !got[name] {
			missing[name] = true
		}
	}

	return demoNames(missing)
}
