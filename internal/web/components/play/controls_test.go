package play_test

// The negative controls: every audit in `play_test.go`, run over a document built
// to violate exactly the rule it claims to check.
//
// # Why this file is not optional
//
// An audit that finds nothing is indistinguishable from an audit that cannot
// look. Phase 8 landed a route package carrying six §10.2 audits, `make a11y`
// printed `ok`, and not one of the six had ever found anything — because
// `go test -run` exits 0 on a pattern matching nothing. Phase 9's own history has
// the sharper version: three rules added in phase 5 had tests that **did not fail**
// when the rule was removed.
//
// So each audit body here runs against a document built to break it, and the test
// requires a finding. Two further requirements, because a finding is not enough:
//
//   - **the recorder must report itself as a helper**, so an audit that calls
//     `t.Fatalf` without `t.Helper()` is caught. The failure would then be
//     attributed to this control rather than to the audit.
//   - **the finding must mention the rule.** An audit that fires for an unrelated
//     reason looks exactly like one that works, so where the rule has a
//     distinctive word in its message this file requires that word.
//
// The recorder is `testing.TB` with its three reporting methods replaced, so the
// audit bodies are the **same functions** rather than copies. A copy for the
// controls is a second answer to the same question and it drifts.

import (
	"fmt"
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/semiplane/semiplane/internal/web/components/play"
)

// recorder is a `testing.TB` that collects failures instead of failing.
type recorder struct {
	testing.TB

	failures []string
	helped   bool
}

// Helper records the call.
func (rec *recorder) Helper() { rec.helped = true }

// Errorf collects one finding.
func (rec *recorder) Errorf(format string, args ...any) {
	rec.failures = append(rec.failures, fmt.Sprintf(format, args...))
}

// Fatalf records one finding and ends the audit.
func (rec *recorder) Fatalf(format string, args ...any) {
	rec.failures = append(rec.failures, fmt.Sprintf(format, args...))

	panic(fatalFinding)
}

// Fatal collects one finding and ends the audit.
func (rec *recorder) Fatal(args ...any) {
	rec.failures = append(rec.failures, fmt.Sprint(args...))

	panic(fatalFinding)
}

// run executes one audit, ending it where `t.Fatalf` would end the real thing.
//
// **A panic that arrived without a finding is re-raised**, so a nil dereference
// inside an audit surfaces as itself rather than as "the audit accepted a document
// built to violate it" — which is a real failure, but a misleading one.
func (rec *recorder) run(audit func(*recorder)) {
	defer func() {
		recovered := recover()
		if recovered != nil && len(rec.failures) == 0 {
			panic(recovered)
		}
	}()

	audit(rec)
}

// fatalFinding ends an audit that called `Fatalf`. A bare sentinel rather than an
// `error`, because `t.Fatalf` ends a test with a panic too and this only has to
// be distinguishable from a real panic — which `recorder.run` handles by requiring
// a finding to have been recorded first.
var fatalFinding = struct{ finding bool }{true}

// runControl runs one audit over one document and requires it to object.
//
// `mentions` is the word the finding has to contain, and it is what separates
// "the audit fired" from "the audit fired about this".
func runControl(t *testing.T, what, mentions string, audit func(*recorder)) {
	t.Helper()

	rec := &recorder{TB: t}

	rec.run(audit)

	if len(rec.failures) == 0 {
		t.Errorf("the %s audit accepted a document built to violate it. An audit "+
			"that cannot fail is not an audit, and this one was checked by building "+
			"exactly the violation it claims to look for", what)
	}

	if !rec.helped {
		t.Errorf("the %s audit never called t.Helper(), so a failure inside it "+
			"would be attributed to this control rather than to the audit", what)
	}

	if mentions != "" {
		joined := strings.Join(rec.failures, "\n")

		if !strings.Contains(joined, mentions) {
			t.Errorf("the %s audit objected, but not about %q. An audit that fires "+
				"for an unrelated reason looks exactly like one that works:\n%s",
				what, mentions, joined)
		}
	}
}

// mutate applies a change to every node in a document, in place.
//
// In place because the audits take a parsed document and a second parse of a
// string would not survive the mutation: the violations below are structural (an
// element that is a `<div>` rather than a `<button>`), and the cheapest way to
// build one is to change one.
func mutate(parsed *html.Node, change func(node *html.Node)) {
	walkAll(parsed, change)
}

// TestEveryAuditObjectsToTheViolationItClaimsTo is the whole of this file.
func TestEveryAuditObjectsToTheViolationItClaimsTo(t *testing.T) {
	t.Parallel()

	t.Run("role=application", func(t *testing.T) {
		t.Parallel()

		parsed := populated(t)
		mutate(parsed, func(node *html.Node) {
			if node.Type == html.ElementNode && node.Data == "section" {
				setAttribute(node, "role", "application")
			}
		})

		runControl(t, "role=application", "role=\"application\"", func(rec *recorder) {
			auditNoRoleApplication(rec, "control", parsed)
		})
	})

	t.Run("a focus stop without the target class", func(t *testing.T) {
		t.Parallel()

		parsed := populated(t)
		mutate(parsed, func(node *html.Node) {
			if node.Type == html.ElementNode && hasClass(node, "token-row") {
				node.Attr = removeClass(node, "target")
			}
		})

		runControl(t, "target class", "target", func(rec *recorder) {
			auditEveryFocusStopCarriesTheTargetClass(rec, "control", parsed)
		})
	})

	t.Run("a positive tabindex", func(t *testing.T) {
		t.Parallel()

		parsed := populated(t)
		mutate(parsed, func(node *html.Node) {
			if node.Type == html.ElementNode && hasClass(node, "token-row") {
				setAttribute(node, "tabindex", "3")
			}
		})

		runControl(t, "positive tabindex", "tabindex", func(rec *recorder) {
			auditNoPositiveTabindex(rec, "control", parsed)
		})
	})

	t.Run("a tabindex a browser reads as positive and a string comparison does not",
		func(t *testing.T) {
			t.Parallel()

			parsed := populated(t)
			mutate(parsed, func(node *html.Node) {
				if node.Type == html.ElementNode && hasClass(node, "token-row") {
					setAttribute(node, "tabindex", " +1 ")
				}
			})

			runControl(t, "positive tabindex", "tabindex", func(rec *recorder) {
				auditNoPositiveTabindex(rec, "control", parsed)
			})
		})

	t.Run("aria-hidden on a focus stop", func(t *testing.T) {
		t.Parallel()

		parsed := populated(t)
		mutate(parsed, func(node *html.Node) {
			if node.Type == html.ElementNode && node.Data == "ul" {
				setAttribute(node, "aria-hidden", "true")
			}
		})

		runControl(t, "aria-hidden", "aria-hidden", func(rec *recorder) {
			auditNoAriaHiddenOnAFocusStop(rec, "control", parsed)
		})
	})

	t.Run("a reference that resolves to nothing", func(t *testing.T) {
		t.Parallel()

		parsed := populated(t)
		mutate(parsed, func(node *html.Node) {
			if node.Type == html.ElementNode && hasClass(node, "token-row") {
				setAttribute(node, "aria-controls", "nowhere")
			}
		})

		runControl(t, "reference integrity", "nowhere", func(rec *recorder) {
			auditEveryReferenceResolves(rec, "control", parsed)
		})
	})

	t.Run("a skipped heading level", func(t *testing.T) {
		t.Parallel()

		parsed := populated(t)
		mutate(parsed, func(node *html.Node) {
			if node.Type == html.ElementNode && node.Data == "h2" {
				node.Data = "h4"
				node.DataAtom = atom.H4
			}
		})

		runControl(t, "heading levels", "<h4>", func(rec *recorder) {
			auditHeadingLevelsNeverSkip(rec, "control", parsed)
		})
	})

	t.Run("two h1 elements", func(t *testing.T) {
		t.Parallel()

		parsed := populated(t)
		mutate(parsed, func(node *html.Node) {
			if node.Type == html.ElementNode && node.Data == "h2" {
				node.Data = "h1"
				node.DataAtom = atom.H1
			}
		})

		runControl(t, "one h1", "<h1>", func(rec *recorder) {
			auditExactlyOneH1(rec, "control", parsed)
		})
	})

	t.Run("two complementary landmarks", func(t *testing.T) {
		t.Parallel()

		parsed := populated(t)
		mutate(parsed, func(node *html.Node) {
			if node.Type == html.ElementNode && node.Data == "footer" {
				setAttribute(node, "role", "complementary")
				setAttribute(node, "aria-label", "Utilities")
			}
		})

		runControl(t, "landmarks", "complementary", func(rec *recorder) {
			auditLandmarksArePresentAndDistinguishing(rec, "control", parsed)
		})
	})

	t.Run("a landmark with no name", func(t *testing.T) {
		t.Parallel()

		parsed := populated(t)
		mutate(parsed, func(node *html.Node) {
			if node.Type == html.ElementNode && node.Data == "aside" {
				setAttribute(node, "aria-label", "")
			}
		})

		runControl(t, "landmarks", "accessible name", func(rec *recorder) {
			auditLandmarksArePresentAndDistinguishing(rec, "control", parsed)
		})
	})

	t.Run("the retired word in a comment", func(t *testing.T) {
		t.Parallel()

		parsed := populated(t)
		parsed.AppendChild(&html.Node{Type: html.CommentNode, Data: " the world map "})

		runControl(t, "vocabulary", "comment", func(rec *recorder) {
			auditNoRetiredVocabulary(rec, "control", parsed)
		})
	})

	t.Run("the retired word in an attribute value", func(t *testing.T) {
		t.Parallel()

		parsed := populated(t)
		mutate(parsed, func(node *html.Node) {
			if node.Type == html.ElementNode && node.Data == "section" {
				setAttribute(node, "aria-label", "the world map")
			}
		})

		runControl(t, "vocabulary", "aria-label", func(rec *recorder) {
			auditNoRetiredVocabulary(rec, "control", parsed)
		})
	})

	t.Run("the retired word in an attribute name", func(t *testing.T) {
		t.Parallel()

		parsed := populated(t)
		mutate(parsed, func(node *html.Node) {
			if node.Type == html.ElementNode && node.Data == "section" {
				setAttribute(node, "data-world", "1")
			}
		})

		runControl(t, "vocabulary", "attribute name", func(rec *recorder) {
			auditNoRetiredVocabulary(rec, "control", parsed)
		})
	})
}

// TestTheKeyboardAuditObjectsToEachWayItCouldBeBroken is the same discipline for
// the DoD claim, which is three assertions in a test rather than an audit body.
//
// The three ways the claim breaks, one per assertion:
//
//   - **a row that is not a `<button>`** — `Enter` does nothing and `Space`
//     scrolls the page;
//   - **a `<button>` with no `type`** — it is a submit button inside a form, and
//     this list is not one;
//   - **a row carrying `tabindex="-1"`** — the row leaves the tab order, so a
//     reader using `Tab` can no longer reach it, which is the property this whole
//     design is built to keep.
func TestTheKeyboardAuditObjectsToEachWayItCouldBeBroken(t *testing.T) {
	t.Parallel()

	cases := []struct {
		what     string
		mentions string
		breakIt  func(node *html.Node)
	}{
		{
			what:     "a row that is not a button",
			mentions: "<button>",
			breakIt: func(node *html.Node) {
				if node.Type == html.ElementNode && hasClass(node, "token-row") {
					node.Data = "div"
					node.DataAtom = atom.Div
				}
			},
		},
		{
			what:     "a button with no type",
			mentions: "type=",
			breakIt: func(node *html.Node) {
				if node.Type == html.ElementNode && hasClass(node, "token-row") {
					removeAttribute(node, "type")
				}
			},
		},
		{
			what:     "a row out of the tab order",
			mentions: "tabindex",
			breakIt: func(node *html.Node) {
				if node.Type == html.ElementNode && hasClass(node, "token-row") {
					node.Attr = append(node.Attr, html.Attribute{
						Key: "tabindex", Val: "-1",
					})
				}
			},
		},
		{
			what:     "a second control inside a row",
			mentions: "focus stops",
			breakIt: func(node *html.Node) {
				if node.Type == html.ElementNode && hasClass(node, "token-row") {
					appendChildElement(node, "a",
						html.Attribute{Key: "href", Val: "/c/greyhaven/play"})
				}
			},
		},
	}

	for _, test := range cases {
		t.Run(test.what, func(t *testing.T) {
			t.Parallel()

			parsed := populated(t)
			mutate(parsed, test.breakIt)

			rows := elementsWith(t, parsed, play.RowChromeHook)
			if len(rows) == 0 {
				t.Fatal("the control fixture renders no rows")
			}

			runControl(t, "keyboard operability", test.mentions, func(rec *recorder) {
				auditRowsAreKeyboardOperable(rec, rows, len(rows))
			})
		})
	}
}

// setAttribute replaces an attribute's value or appends it, and is what a
// violation needs where a naive append would do nothing.
//
// **Because an HTML parser keeps the first of a repeated attribute.** A control
// that appended `role="complementary"` to a `<footer role="contentinfo">` would
// build a document in which that footer still reads `contentinfo`, and the audit
// would be right to say nothing. Two controls started life that way and both were
// measuring the mutation rather than the audit.
func setAttribute(node *html.Node, key, value string) {
	for index, attr := range node.Attr {
		if attr.Key == key {
			node.Attr[index].Val = value

			return
		}
	}

	node.Attr = append(node.Attr, html.Attribute{Key: key, Val: value})
}

// removeAttribute drops an attribute if it is present.
func removeAttribute(node *html.Node, key string) {
	kept := make([]html.Attribute, 0, len(node.Attr))

	for _, attr := range node.Attr {
		if attr.Key != key {
			kept = append(kept, attr)
		}
	}

	node.Attr = kept
}

// appendChildElement adds a child element to a node, with the attributes given.
func appendChildElement(node *html.Node, name string, attrs ...html.Attribute) {
	child := &html.Node{
		Type:     html.ElementNode,
		Data:     name,
		DataAtom: atom.Lookup([]byte(name)),
		Attr:     attrs,
	}

	node.AppendChild(child)
}

// removeClass returns an element's attributes with one class removed.
func removeClass(node *html.Node, class string) []html.Attribute {
	kept := make([]html.Attribute, 0, len(node.Attr))

	for _, attr := range node.Attr {
		if attr.Key != "class" {
			kept = append(kept, attr)

			continue
		}

		names := strings.Fields(attr.Val)

		reduced := names[:0]

		for _, name := range names {
			if name != class {
				reduced = append(reduced, name)
			}
		}

		attr.Val = strings.Join(reduced, " ")
		kept = append(kept, attr)
	}

	return kept
}
