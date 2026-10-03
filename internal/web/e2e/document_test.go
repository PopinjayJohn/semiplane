package e2e_test

// The fixture: the real router, the real handler, the real hub, the real engine.
//
// # Why an in-process server rather than a fake
//
// Every claim in `play_test.go` is about **the document the product serves**, and
// three of the four ways that can go wrong are not in any component:
//
//   - a script element written at a call site the audit does not read;
//   - a module embedded but never referenced;
//   - a route mounted behind a gate the document asserts nothing about.
//
// A fixture that composes the components by hand sees none of them. This one calls
// `play.Mount`, so the gate, the pattern, the title and the `Vary` header are the
// product's own — and a claim about "the play page" means the play page.
//
// # Why it is in this package and not `internal/httpapi/play`
//
// That package's `harness_test.go` already builds a harness for the *route*, and
// this one for the *page*: the difference is that this one fetches over HTTP and
// parses, so the assertions are about bytes that crossed a socket. It is also why
// the test package is `e2e` and not `play_test` — an assertion that says "the play
// page" should not live inside the package that writes the play page, where a
// reader would reasonably assume the two agree by construction.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// staticJSRoot is the directory the drift guard reads.
//
// `../static/js` rather than an embed, because the claim under test is about the
// working tree — see `moduleDirectories`.
var staticJSRoot = filepath.Join("..", "static", "js")

// servedDocument is one fetched play page: the parsed tree, the raw body for the
// byte-identity checks, and every URL the document references.
//
// `references` is collected rather than searched on demand, so a module is "served"
// if the document names it **anywhere** — `src`, `href`, or a preload — rather than
// only in the element type a particular version of this file happened to expect.
type servedDocument struct {
	root       *html.Node
	body       string
	references []string
}

// parsePlayDocument fetches `/c/{slug}/play` as the campaign's GM and parses it.
//
// **Failing rather than skipping when the response is not a document.** §10.2's
// rules are claims about a document; a response that is not one means the route
// answered something else entirely, and a test that skipped would report green
// about a page nobody received.
func parsePlayDocument(t *testing.T) servedDocument {
	t.Helper()

	server, _ := startTabletop(t)

	response := fetch(t, server, gmUserID, "/c/"+gmSlug+"/play")

	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /c/%s/play = %d, want 200. The document is the tabletop's "+
			"only HTML surface and a non-200 means every assertion below is about "+
			"something no reader received", gmSlug, response.StatusCode)
	}

	body, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		t.Fatalf("read the play document: %v", readErr)
	}

	if closeErr := response.Body.Close(); closeErr != nil {
		t.Errorf("close the play document: %v", closeErr)
	}

	root, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("parse the play document: %v. §10.2's rules are about a parsed "+
			"tree, and a walk that fell back to raw text would read the reasoning "+
			"in this repository's comments as markup", err)
	}

	return servedDocument{
		root:       root,
		body:       string(body),
		references: collectReferences(root),
	}
}

// collectReferences is every URL the document points a browser at.
func collectReferences(root *html.Node) []string {
	var found []string

	eachElement(root, func(node *html.Node) {
		for _, attribute := range []string{"src", "href", "data-socket"} {
			if value := attrOf(node, attribute); value != "" {
				found = append(found, value)
			}
		}
	})

	return found
}

// eachElement visits every **element** in document order.
//
// Document order and not tree order, because an outline rule and a landmark rule
// both reason about what a reader meets, and a walk that descended first would
// judge a document by its nesting.
func eachElement(node *html.Node, visit func(*html.Node)) {
	if node.Type == html.ElementNode {
		visit(node)
	}

	for child := node.FirstChild; child != nil; child = child.NextSibling {
		eachElement(child, visit)
	}
}

// attrOf reads an attribute, treating absent and empty alike.
//
// An element with an empty `tabindex` and one with no `tabindex` are the same
// element to a browser, and the rules in `play_test.go` are about behaviour rather
// than about spelling — so `hasAttr` is where the distinction matters and it is
// used there.
func attrOf(node *html.Node, name string) string {
	for _, attribute := range node.Attr {
		if attribute.Key == name {
			return attribute.Val
		}
	}

	return ""
}

// hasAttr reports whether the attribute is present at all, empty or not.
func hasAttr(node *html.Node, name string) bool {
	for _, attribute := range node.Attr {
		if attribute.Key == name {
			return true
		}
	}

	return false
}

// readDirSorted is `os.ReadDir` with the determinism this repository's rule code
// forbids producing by accident.
//
// Rule code must be deterministic — no map iteration, which is a lint rule here —
// and a test that ranged a directory and reported whichever entry came first would
// make a *failure message* non-deterministic, which is how a flaky test gets
// mistaken for a flaky product.
func readDirSorted(root string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err //nolint:wrapcheck // the caller's message names the path
	}

	slices.SortFunc(entries, func(a, b os.DirEntry) int {
		return strings.Compare(a.Name(), b.Name())
	})

	return entries, nil
}

// fetch issues one request as a member of the named campaign.
func fetch(t *testing.T, server *httptest.Server, userID int64, path string) *http.Response {
	t.Helper()

	request, err := http.NewRequestWithContext(
		context.Background(), http.MethodGet, server.URL+path, http.NoBody,
	)
	if err != nil {
		t.Fatalf("build the request for %s: %v", path, err)
	}

	request.Header.Set(userHeader, strconv.FormatInt(userID, 10))

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}

	t.Cleanup(func() {
		if err := response.Body.Close(); err != nil {
			t.Errorf("close the response for %s: %v", path, err)
		}
	})

	return response
}
