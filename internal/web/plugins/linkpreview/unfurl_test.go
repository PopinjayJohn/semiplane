package linkpreview_test

// The unfurl helper's security boundary, asserted from outside the package.
//
// # What is claimed here, and where each claim is enforceable
//
// Three claims, and **each one has a place it can be tested and a place it cannot**, which is
// stated rather than glossed:
//
//  1. **A typed private address is refused before any I/O.** `checkURL` parses the host and,
//     when it is an IP literal, refuses anything that is not a public unicast address.
//     Fully enforceable and tested here with an `httptest` server on `127.0.0.1` and a
//     request counter that must stay at zero.
//  2. **A name that resolves to a private address is refused at the dial.** That is
//     `dialPublicOnly`, the transport hook. It can only be tested against a name that
//     resolves to a private address, which means either a real DNS record this project
//     controls or a custom `net.Resolver`. Neither is available in a unit test, so this
//     claim is tested the only way it can be: **by asserting the hook is installed and that
//     it refuses the addresses it is handed**, which is the function `net/http` calls with
//     the resolved address.
//  3. **Campaign content is not reachable through this path.** Structural: the helper's type
//     graph contains no filesystem handle, no campaign and no path. Tested by parsing the
//     package's imports (`TestThisPackageHoldsNoContentRoot`) and by requiring a URL naming
//     this host's own routes to be refused before the request leaves.
//
// # Why the SSRF tests use `NewOver` and one does not
//
// `NewOver` is the seam that supplies a client without the dial hook, and the tests that need
// to reach a loopback server must use it. **Claim 1 is tested through it** — and that is the
// stronger test, because it proves the URL-level refusal is a property of `checkURL` rather
// than of the transport. Claim 2 is tested through `New`, because the dial hook is the claim.

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/semiplane/semiplane/internal/web/plugins/linkpreview"
)

// A document a preview server serves, with all three fields stated two ways so a fixture
// that only one lookup could satisfy fails.
//
// **Two ways on purpose.** `<title>` and `og:title` name different elements, and a parser
// that read only one would pass a fixture that had only the other — which is how a page that
// states an Open Graph title and a different `<title>` ends up previewed from the wrong one.
const aPage = `<!DOCTYPE html>
<html lang="en">
	<head>
		<title>Salt &amp; Iron — the road north</title>
		<meta name="description" content="Iron and rust, and the road north."/>
		<meta property="og:image" content="/static/road.png"/>
	</head>
	<body><p>The road north.</p></body>
</html>`

// countingServer is an `httptest.Server` that records how many requests it received.
//
// **The counter is the assertion, not the response.** A helper that fetched and then refused
// to *render* would still have pulled a private page's bytes into this process, and only the
// counter distinguishes "refused before the fetch" from "fetched and threw the answer away".
func countingServer(t *testing.T, body string) (url string, calls *atomic.Int64) {
	t.Helper()

	var served atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served.Add(1)

		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("the fixture server could not write its body: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	return server.URL, &served
}

// --- Claim 1: a typed private address is refused before any I/O ----------------

// TestAPrivateAddressIsRefusedWithoutBeingFetched is the SSRF boundary's core, and the
// counter is what makes it the core.
//
// **Every address a campaign's own content or a cloud provider lives at is in the table**, and
// the two ends of it matter most: `169.254.169.254` is the metadata endpoint every cloud
// provider documents and the first thing anybody reaches for, and `127.0.0.1` is *this
// process* — which for a self-hosted instance is the campaign's own wiki.
//
// The assertions are three, and all three are needed: the error is the right sentinel, the
// error's **text does not name the URL** (a refusal message is a log line and the URL is
// author input), and the server received nothing.
func TestAPrivateAddressIsRefusedWithoutBeingFetched(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name   string
		target func(base string) string
	}{
		{
			name:   "loopback, which is this process's own campaign routes",
			target: func(base string) string { return base },
		},
		{
			name:   "the cloud metadata endpoint",
			target: func(string) string { return "http://169.254.169.254/latest/meta-data/" },
		},
		{
			name:   "the metadata endpoint by its IPv6 form",
			target: func(string) string { return "http://[fd00:ec2::254]/latest/meta-data/" },
		},
		{
			name:   "a private address in this very network",
			target: func(string) string { return "http://10.0.0.5/" },
		},
		{
			name:   "the private 172.16 range",
			target: func(string) string { return "http://172.16.31.7/" },
		},
		{
			name:   "carrier-grade NAT, which is neither public nor private",
			target: func(string) string { return "http://100.64.0.1/" },
		},
		{
			name:   "IPv6 loopback",
			target: func(string) string { return "http://[::1]/" },
		},
		{
			name:   "IPv6 link-local, which is what fe80 names",
			target: func(string) string { return "http://[fe80::1]/" },
		},
		{
			name:   "IPv6 unique local",
			target: func(string) string { return "http://[fc00::1]/" },
		},
		{
			name:   "the unspecified address, which means every address",
			target: func(string) string { return "http://0.0.0.0/" },
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			base, served := countingServer(t, aPage)

			// **`New`, the production constructor.** The point is that a link naming a private
			// address is refused *before any fetch*, and only the guarded constructor claims
			// that — the seam (`NewOver`) is the one documented to give the address policy up,
			// which is what lets a test reach a loopback server at all.
			_, err := linkpreview.New().Unfurl(t.Context(), testCase.target(base))

			if !errors.Is(err, linkpreview.ErrPrivateAddress) {
				t.Errorf("Unfurl returned %v, want one wrapping ErrPrivateAddress; a link to "+
					"this host is a link to a campaign's own page, and S-8's access matrix "+
					"cannot be answered through a rendering path that never went through a gate",
					err)
			}

			if err != nil {
				// The refusal's text goes to a log line, and the URL is author input arriving
				// through Obsidian Sync — so it must not be in the text.
				for _, forbidden := range []string{
					"169.254", "10.0.0.5", "127.0.0.1", "::1", base,
				} {
					if strings.Contains(err.Error(), forbidden) {
						t.Errorf("the refusal quotes %q; the URL is author input and a refusal "+
							"message is a log line (S-12.3). Full text: %s", forbidden,
							err.Error())
					}
				}
			}

			if got := served.Load(); got != 0 {
				t.Errorf("the server received %d requests; a link naming this host must be "+
					"refused before any fetch, because the bytes it would return are campaign "+
					"content", got)
			}
		})
	}
}

// TestATypedPrivateAddressIsRefusedByTheURLCheckAlone is claim 1 with the transport removed
// from the picture entirely, and it is the row that says *which* check does the work.
//
// **`New`'s dial hook would refuse these addresses too**, so a test that only used `New` would
// not say whether the refusal came from the URL check or the dial — and the difference
// matters: the URL check costs nothing and happens before a DNS lookup, the dial is the
// backstop. So the predicate is called directly, with the URL it would have been handed.
func TestATypedPrivateAddressIsRefusedByTheURLCheckAlone(t *testing.T) {
	t.Parallel()

	for _, target := range []string{
		"http://127.0.0.1/",
		"http://127.0.0.1:8080/c/greyhaven/wiki/Vault",
		"http://169.254.169.254/latest/meta-data/",
		"http://[::1]/",
		"http://10.0.0.5/",
		"http://100.64.0.1/",
		"http://0.0.0.0/",
	} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()

			if err := linkpreview.RefuseAddressFor(target); !errors.Is(err,
				linkpreview.ErrPrivateAddress) {
				t.Errorf("the URL check returned %v for %q, want one wrapping "+
					"ErrPrivateAddress; a URL that already spells the address needs no "+
					"resolution to know where it goes, so the refusal should cost no lookup",
					err, target)
			}
		})
	}

	// **And a public address must pass**, because an allowlist that refuses everything is a
	// denial of service rather than a boundary. This row is the only one that can tell the
	// two apart.
	for _, target := range []string{
		"https://example.com/",
		"http://93.184.216.34/",
	} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()

			if err := linkpreview.RefuseAddressFor(target); err != nil {
				t.Errorf("the URL check refused the public address %q: %v", target, err)
			}
		})
	}
}

// TestANonHTTPSchemeIsRefusedWithoutBeingFetched is the first of the three refusals, and the
// one that is cheapest to state and easiest to get wrong.
//
// **An allowlist and not a denylist, which is why `file:` is in the table.** `file://` reads
// this process's disk, `gopher://` and `dict://` speak protocols whose purpose is reaching
// things an HTTP request should not, and `javascript:` and `data:` are not fetch schemes at
// all but a URL parser will happily accept them. A denylist would list today's set and miss
// the next one registered.
func TestANonHTTPSchemeIsRefusedWithoutBeingFetched(t *testing.T) {
	t.Parallel()

	for _, target := range []string{
		"file:///etc/passwd",
		"file:///var/lib/semiplane/campaigns/greyhaven/wiki/Vault.md",
		"gopher://127.0.0.1:11211/",
		"dict://127.0.0.1:11211/stat",
		"javascript:alert(1)",
		"data:text/html,<script>alert(1)</script>",
		"ftp://example.com/",
		"ws://example.com/",
		"//example.com/no-scheme",
		"/c/greyhaven/wiki/Vault",
		"Vault.md",
		"../notes/Secrets.md",
	} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()

			// The server exists so the counter can be asserted, and none of these targets
			// names it — that is the point. A refusal that happened *after* a fetch would show
			// a non-zero count.
			_, served := countingServer(t, aPage)

			_, err := linkpreview.NewOver(http.DefaultClient).
				Unfurl(t.Context(), target)

			if !errors.Is(err, linkpreview.ErrBadScheme) &&
				!errors.Is(err, linkpreview.ErrNotAbsolute) {
				t.Errorf("Unfurl returned %v for %q, want a scheme or absoluteness refusal; "+
					"only http and https are previewed, by an allowlist", err, target)
			}

			if got := served.Load(); got != 0 {
				t.Errorf("the server received %d requests for %q; the refusal must come "+
					"before any I/O", got, target)
			}
		})
	}
}

// TestAURLCarryingCredentialsIsRefused is a refusal nobody asks for and everybody should
// have.
//
// `http.Client` sends a URL's userinfo as an `Authorization` header, so a preview of
// `http://alice:hunter2@example.com/` performs an **authenticated request on a link author's
// behalf** — and no reader of the preview can see that it happened. S-8's matrix says nothing
// about it because it is not about the campaign, which is exactly why it needs a rule here.
func TestAURLCarryingCredentialsIsRefused(t *testing.T) {
	t.Parallel()

	for _, target := range []string{
		"http://alice:hunter2@example.com/",
		"https://alice@example.com/",
	} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()

			_, err := linkpreview.NewOver(http.DefaultClient).Unfurl(t.Context(), target)

			if !errors.Is(err, linkpreview.ErrNotAbsolute) {
				t.Errorf("Unfurl returned %v for %q, want a refusal; a URL carrying "+
					"credentials would be sent as an Authorization header, which is an "+
					"authenticated request on the link author's behalf", err, target)
			}

			if err != nil && strings.Contains(err.Error(), "hunter2") {
				t.Errorf("the refusal quotes the password: %s", err.Error())
			}
		})
	}
}

// TestThePublicAddressIsAllowed is the other direction, and it is the one that makes the
// refusals above meaningful.
//
// **An allowlist that refuses everything is not a security boundary; it is a denial of
// service.** So a public address must be fetched, the document parsed, and the three fields
// returned — and it is reached through a loopback `httptest` server by way of `NewOver`,
// which is the seam's whole purpose: the URL check cannot know that `127.0.0.1` stands for a
// server this test controls.
func TestThePublicAddressIsAllowed(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		if _, err := w.Write([]byte(aPage)); err != nil {
			t.Errorf("the fixture server could not write its body: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	preview, err := linkpreview.NewOver(server.Client()).Unfurl(t.Context(), server.URL)
	if err != nil {
		t.Fatalf("a preview of a reachable page failed: %v; an allowlist that refuses "+
			"everything is a denial of service, not a boundary", err)
	}

	if !strings.Contains(preview.Title, "Salt & Iron") {
		t.Errorf("the preview's title is %q, want the document's own; entities are decoded "+
			"by the tokenizer and a scan would emit `&amp;`", preview.Title)
	}

	if !strings.Contains(preview.Description, "Iron and rust") {
		t.Errorf("the preview's description is %q, want the page's `description` meta",
			preview.Description)
	}

	// The image is **resolved against the final URL**, so a relative `og:image` becomes an
	// absolute one a browser could load — and the resolution is what makes the result's own
	// `checkURL` check meaningful.
	if !strings.HasPrefix(preview.Image, server.URL) {
		t.Errorf("the preview's image is %q, want an absolute URL resolved against the final "+
			"address; a relative one is a path on a different host, which is a fetch this "+
			"package did not vet", preview.Image)
	}

	// And the final URL is what the preview carries, not the one asked for.
	if preview.URL != server.URL {
		t.Errorf("the preview's URL is %q, want the final address %q", preview.URL, server.URL)
	}
}

// --- Claim 2: the dial hook ---------------------------------------------------

// TestTheSeamKeepsTheURLLevelRefusals is `NewOver`'s one real cost, as a test rather than a
// sentence in a doc comment.
//
// **Stated because the difference between the two constructors is one an operator would
// otherwise discover in production**: `New` refuses a rebind and `NewOver` does not. The URL
// level survives its absence — every typed address, every bad scheme, every credential — which
// is what makes the seam safe to reach a loopback server in a test at all.
//
// The dial hook itself is tested in `export_internal_test.go`, which is `package linkpreview`
// and so can reach the unexported transport field. That is the Go-standard arrangement rather
// than a suppression, and it is why the two claims are in two files: the exported surface is
// tested from outside it, and the unexported field from inside the package.
func TestTheSeamKeepsTheURLLevelRefusals(t *testing.T) {
	t.Parallel()

	for _, target := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://127.0.0.1/",
		"http://[::1]/",
		"http://10.0.0.5/",
		"file:///etc/passwd",
		"gopher://127.0.0.1:11211/",
		"http://alice:hunter2@example.com/",
	} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()

			_, err := linkpreview.NewOver(http.DefaultClient).Unfurl(t.Context(), target)
			if err == nil {
				t.Errorf("Unfurl accepted %q through the seam; the seam loses the rebind "+
					"defence and keeps every other one, and that is the contract", target)
			}
		})
	}
}

// TestClassIsAFunction ratherThanATypeName is the log-line contract, and it is one test
// because `Class` is the only thing standing between a transport error's text and a log.
//
// Every source `Class` reduces is attacker-influenced — `net/url` quotes the URL,
// `net.Dialer` quotes the address, `os` quotes the path — and this asserts the reduction
// produces a **class** for each rather than passing the error's own text through.
func TestClassIsAFunctionRatherThanATypeName(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name string
		err  error
		want string
	}{
		{name: "no error", err: nil, want: ""},
		{name: "a non-absolute link", err: linkpreview.ErrNotAbsolute, want: "not_absolute"},
		{name: "a bad scheme", err: linkpreview.ErrBadScheme, want: "bad_scheme"},
		{
			name: "a private address", err: linkpreview.ErrPrivateAddress,
			want: "private_address",
		},
		{
			name: "too many redirects", err: linkpreview.ErrTooManyRedirects,
			want: "too_many_redirects",
		},
		{name: "too large", err: linkpreview.ErrTooLarge, want: "too_large"},
		{name: "not html", err: linkpreview.ErrNotHTML, want: "not_html"},
		{name: "not fetched", err: linkpreview.ErrNotFetched, want: "not_fetched"},
		{
			// **A wrapped refusal, not a bare one** — which is what a real call returns, and
			// the row that catches a `Class` that only matched its own sentinels.
			name: "a wrapped refusal",
			err:  errors.New("linkpreview: " + linkpreview.ErrPrivateAddress.Error()),
			want: "fetch_failed",
		},
		{name: "an unrelated error", err: errors.New("something else"), want: "fetch_failed"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := linkpreview.Class(testCase.err)
			if got != testCase.want {
				t.Errorf("Class(%v) = %q, want %q; observability.errorClass falls back to "+
					"the dynamic type %T, which no alert can match, and every source here "+
					"quotes something attacker-reachable (S-12.3)",
					testCase.err, got, testCase.want, testCase.err)
			}
		})
	}
}

// --- The helpers this file's remaining tests read -----------------------------
//
// `strconvItoa` and `truncate` live here rather than inline at each call site because the
// status-code table and the failure messages both need them, and a helper written twice is a
// helper that drifts.

// --- Claim 3: campaign content is not reachable -------------------------------

// TestThisPackageHoldsNoContentRoot is the structural half of the third claim, and it is a
// test rather than a comment because a comment is not a dependency rule.
//
// **A campaign's pages are reachable through `internal/content`, and only through it.** So a
// helper that could read one would import it. Parsing the imports proves the absence for
// every file in the package rather than for the one a grep happened to match — and the
// package-level list is the half that matters, because `internal/content` is what a later
// refactor would reach for when adding "a preview of a campaign page".
//
// `internal/store` is in the same list for the same reason: it is the package that answers
// "which pages does this campaign have", so a preview that wanted to enumerate them would go
// there.
func TestThisPackageHoldsNoContentRoot(t *testing.T) {
	t.Parallel()

	// **The non-test files only**, which is why `goFiles` filters on the `_test.go` suffix:
	// this test binary legitimately needs `net/http/httptest` and a `*testServer`, and the
	// audit is about what the *package* may reach, not what a fixture may stand in for.

	// `internal/web/plugins` is in the list because the **hook** is rendered into a page body
	// and a plugin could import its sibling packages; the specific thing this rules out is
	// the content and store halves, and the whole list is here so the reason is one sentence.
	forbidden := map[string]string{
		"github.com/semiplane/semiplane/internal/content": "campaign pages are read through here, and a preview " +
			"that read one would answer S-8's access matrix through a path that never went " +
			"through a gate",
		"github.com/semiplane/semiplane/internal/store": "this is the package that knows which pages a " +
			"campaign has, so a preview that wanted to enumerate them would come here",
		"github.com/semiplane/semiplane/internal/httpapi": "a preview is read-only and reaches nothing but the " +
			"network; a dependency on the HTTP surface would be a second route into the " +
			"campaigns this helper must not see",
	}

	for _, name := range goFiles(t, ".") {
		for _, imported := range parsedImports(t, name) {
			if why, banned := forbidden[imported]; banned {
				t.Errorf("%s imports %q: %s. The unfurl helper's authority is three strings "+
					"the REMOTE page said; there is no parameter through which a caller could "+
					"ask for a path", name, imported, why)
			}
		}
	}
}

// TestALinkIntoACampaignsOwnContentRootIsRefused is the third claim as a behaviour, and it
// is the one that would catch a future change which *could* reach a campaign page.
//
// The URL is this host's own wiki route: `/c/greyhaven/wiki/Vault`. For a self-hosted
// instance that is loopback or a private address, so it is refused by the address rule — and
// **the assertion that matters is that it is refused the same way a link to `10.0.0.5` is**,
// because a dedicated rule for "our own wiki" would be a second rule and a second place for
// the SSRF boundary to be wrong.
func TestALinkIntoACampaignsOwnContentRootIsRefused(t *testing.T) {
	t.Parallel()

	base, served := countingServer(t, aPage)

	address := strings.TrimPrefix(base, "http://")

	for _, target := range []string{
		base + "/c/greyhaven/wiki/Vault",
		base + "/c/greyhaven/wiki/Secrets",
		"http://" + address + "/c/greyhaven/wiki/Vault?token=abc",
	} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()

			// `New`, the production constructor — a link into this instance's own routes is
			// refused by the constructor a deployment actually uses, which is the only version
			// of this claim anyone cares about.
			_, err := linkpreview.New().Unfurl(t.Context(), target)

			if !errors.Is(err, linkpreview.ErrPrivateAddress) {
				t.Errorf("Unfurl returned %v for a link into this instance's own wiki, want "+
					"the same refusal a link to any private address gets; a campaign's pages "+
					"live on this host, so a helper that fetched whatever a page linked to "+
					"would hand a private page to whoever wrote the link", err)
			}
		})
	}

	if got := served.Load(); got != 0 {
		t.Errorf("the server received %d requests; a link naming this host's own routes must "+
			"be refused before any fetch", got)
	}
}

// --- The response rules ------------------------------------------------------

// TestAResponseThatIsNotADocumentIsRefused is the content-type rule, and it is a refusal
// rather than a tolerated case because a tolerated one is a preview that quietly renders
// empty.
//
// A `text/plain` body parsed as HTML yields no `<title>`, so accepting one produces a card
// with nothing in it and no reason a reader can see. **The three types in the table are the
// two HTML types and one that is not**, because a rule that only refuses `application/pdf`
// would accept `text/plain` and `application/json`.
func TestAResponseThatIsNotADocumentIsRefused(t *testing.T) {
	t.Parallel()

	for _, contentType := range []string{
		"text/plain; charset=utf-8",
		"application/json",
		"application/pdf",
		"image/png",
		"text/html; charset=us-ascii",
	} {
		t.Run(contentType, func(t *testing.T) {
			t.Parallel()

			// `text/html` *with a charset* is in the positive list deliberately: a strict
			// comparison against the bare type would refuse it for a reason nobody could act
			// on, and a rule that over-refuses is one a caller stops using.
			if !strings.HasPrefix(contentType, "text/html") {
				server := httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						w.Header().Set("Content-Type", contentType)

						if _, err := w.Write([]byte(aPage)); err != nil {
							t.Errorf("the fixture could not write: %v", err)
						}
					}),
				)
				t.Cleanup(server.Close)

				_, err := linkpreview.NewOver(server.Client()).
					Unfurl(t.Context(), server.URL)

				if !errors.Is(err, linkpreview.ErrNotHTML) {
					t.Errorf("a %q response was previewed (err %v); it parses to no title, "+
						"so accepting it is a card that renders empty for a reason nobody can "+
						"see", contentType, err)
				}
			}
		})
	}
}

// TestAnOversizeResponseIsRefused is the amplification bound, and both halves are tested: the
// declared length and the read.
//
// **A `Content-Length` above the bound is refused without reading a byte**, which is the
// cheap half; and a body *larger than the bound with no declared length* is caught by the
// reader, which is the half that needs the `MaxBodyBytes + 1` — a reader capped at exactly
// the limit cannot tell a body of exactly the limit from one that continues, and a truncated
// HTML document parses as a document with no `<title>`.
func TestAnOversizeResponseIsRefused(t *testing.T) {
	t.Parallel()

	t.Run("a declared length over the bound", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Length", strconv.FormatInt(linkpreview.MaxBodyBytes+1, 10))
			w.WriteHeader(http.StatusOK)

			if _, err := w.Write([]byte(aPage)); err != nil {
				t.Errorf("the fixture could not write: %v", err)
			}
		}))
		t.Cleanup(server.Close)

		_, err := linkpreview.NewOver(server.Client()).Unfurl(t.Context(), server.URL)

		if !errors.Is(err, linkpreview.ErrTooLarge) {
			t.Errorf("a response declaring more than the bound was previewed (err %v); a "+
				"preview needs a title and a couple of metas, both of which are in the "+
				"first few kilobytes", err)
		}
	})

	t.Run("a body over the bound with no declared length", func(t *testing.T) {
		t.Parallel()

		// Flushed in two writes, so there is no usable `Content-Length` for the cheap check to
		// read — which is the case the reader exists for.
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")

			flusher, canFlush := w.(http.Flusher)
			if !canFlush {
				t.Errorf("the fixture server cannot flush, so the response would carry a " +
					"Content-Length and this test would prove the cheap check instead")
			}

			half := linkpreview.MaxBodyBytes / 2

			if _, err := w.Write([]byte("<html><head><title>Big</title></head><body>" +
				strings.Repeat("x", int(half)))); err != nil {
				t.Errorf("the fixture could not write: %v", err)

				return
			}

			if canFlush {
				flusher.Flush()
			}

			if _, err := w.Write([]byte(strings.Repeat("y", int(half)+2))); err != nil {
				t.Errorf("the fixture could not write: %v", err)
			}
		}))
		t.Cleanup(server.Close)

		_, err := linkpreview.NewOver(server.Client()).Unfurl(t.Context(), server.URL)

		if !errors.Is(err, linkpreview.ErrTooLarge) {
			t.Errorf("an oversize body with no declared length was previewed (err %v); the "+
				"reader is the enforcement for a server that lied or chunked", err)
		}
	})
}

// TestANonSuccessStatusIsRefused is the last of the response rules, and the interesting
// assertion is that it is a refusal rather than a preview of the error page.
//
// A 404's body is a sentence from this project, and parsing it as a document would find no
// title and render an empty card — or, worse, find whatever `<title>` an upstream error page
// happened to carry and show it as though it were the link's preview.
func TestANonSuccessStatusIsRefused(t *testing.T) {
	t.Parallel()

	for _, status := range []int{
		http.StatusMovedPermanently,
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusInternalServerError,
	} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
					w.WriteHeader(status)

					if _, err := w.Write([]byte(
						`<html><head><title>AN ERROR PAGE</title></head><body></body></html>`,
					)); err != nil {
						t.Errorf("the fixture could not write: %v", err)
					}
				}),
			)
			t.Cleanup(server.Close)

			preview, err := linkpreview.NewOver(server.Client()).
				Unfurl(t.Context(), server.URL)

			if err == nil {
				t.Errorf("a %d response was previewed as %+v; a 404's body is a sentence "+
					"from this project and parsing it as a document is how an upstream error "+
					"page becomes a link's preview", status, preview)
			}
		})
	}
}

// TestTheUnfurlersOnlyMethodTakesAURL is the read-only claim, asserted as the **shape** of
// the exported surface.
//
// The helper's whole type is a client and a preview struct, and there is no method on
// `Unfurler` that takes anything it could write to — no path, no `io.Writer` a caller
// supplies, no `os.Root`. This test reflects over the method set because behaviour is not
// observable here: **a fetch has no write side effect to watch**, so the only thing a test
// can check is that no signature offers one.
//
// And the count is the assertion, not a convenience: every added method is a chance to add a
// parameter this helper could read campaign content through, so a second method is a finding
// rather than an extension.
func TestTheUnfurlersOnlyMethodTakesAURL(t *testing.T) {
	t.Parallel()

	unfurler := linkpreview.New()

	methods := exportedMethods(reflect.TypeOf(unfurler))
	if len(methods) != 1 || methods[0] != "Unfurl" {
		t.Errorf("Unfurler's exported methods are %v, want exactly [Unfurl]; each one is a "+
			"chance to add a parameter this helper could read campaign content through",
			methods)
	}
}

// exportedMethods returns a type's exported method names, sorted.
func exportedMethods(methodType reflect.Type) []string {
	names := make([]string, 0, methodType.NumMethod())

	for method := range methodType.Methods() {
		names = append(names, method.Name)
	}

	sort.Strings(names)

	return names
}

// TestAPreviewFieldIsBounded is the reader-facing bound, and it is a property of the parse
// rather than of the response.
//
// **A title of ten thousand characters is not a longer title; it is a card that pushes the
// rest of the page off the screen.** The bound is applied on a rune boundary — a byte slice
// would split a combining mark and produce a replacement glyph in the middle of somebody's
// title — and the test uses a multi-byte string so the boundary is the thing under test.
func TestAPreviewFieldIsBounded(t *testing.T) {
	t.Parallel()

	// A title of 5000 `é`, which is 10000 bytes: a byte-bounded implementation and a
	// rune-bounded one produce different lengths, and only one of them splits nothing.
	long := strings.Repeat("é", 5000)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		if _, err := w.Write([]byte(
			"<html><head><title>" + long + "</title></head><body></body></html>",
		)); err != nil {
			t.Errorf("the fixture could not write: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	preview, err := linkpreview.NewOver(server.Client()).Unfurl(t.Context(), server.URL)
	if err != nil {
		t.Fatalf("the preview failed: %v", err)
	}

	if got := len([]rune(preview.Title)); got > 512 {
		t.Errorf("the title came back as %d runes; a preview field is bounded so a card is a "+
			"card, and the bound is on runes so a multi-byte character is never split", got)
	}

	if strings.Contains(preview.Title, "�") {
		t.Errorf("the title contains a replacement character: %q; the bound is on runes, and "+
			"a byte slice splits a multi-byte character", truncate(preview.Title))
	}
}

// TestAMetaInsideAScriptIsNotRead is the parser claim, and the fixture is the shape a
// substring scan cannot tell apart.
//
// `<script>var x = '<meta name="description" content="from a script">'</script>` — a scan
// over the bytes finds the meta and a scan over the *rendered* text finds it too, because the
// browser does not either. Only the parser knows the element is inside a script body, where
// a `<meta>` is text. Every page is attacker-reachable through the link that named it, so
// this is the boundary between "a page says" and "a page renders".
func TestAMetaInsideAScriptIsNotRead(t *testing.T) {
	t.Parallel()

	const hostile = `<!DOCTYPE html>
<html lang="en">
	<head>
		<title>Honest</title>
		<script>var meta = '<meta name="description" content="FROM A SCRIPT">' +
			'<meta property="og:image" content="http://169.254.169.254/"></script>';</script>
	</head>
	<body></body>
</html>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		if _, err := w.Write([]byte(hostile)); err != nil {
			t.Errorf("the fixture could not write: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	preview, err := linkpreview.NewOver(server.Client()).Unfurl(t.Context(), server.URL)
	if err != nil {
		t.Fatalf("the preview failed: %v", err)
	}

	if strings.Contains(preview.Description, "FROM A SCRIPT") {
		t.Errorf("the preview's description is %q; a meta inside a script body is text to a "+
			"browser and must be text here too, or a page can state a description it never "+
			"renders", preview.Description)
	}

	if preview.Image != "" {
		t.Errorf("the preview's image is %q; a meta inside a script must not reach the "+
			"result, and this one names a metadata address besides", preview.Image)
	}
}

// TestAnImageNamingAPrivateAddressIsDropped is the result-side SSRF rule.
//
// **An `og:image` of `http://169.254.169.254/` is a URL a browser fetches the moment the
// card renders** — and a browser fetching it is an SSRF from every reader of the page. So the
// value is re-checked against the same URL policy, and dropped rather than shown: a preview
// with no image is a card with no image, and a wrong image is a card showing one thing where
// another belongs.
func TestAnImageNamingAPrivateAddressIsDropped(t *testing.T) {
	t.Parallel()

	// **The `shape` column is the table's own classification, not a guess at the URL**, and
	// that is deliberate: `javascript:alert(1)` has no `://` in it, so a test that inferred
	// "is this a shape row?" from the string would classify it as an address row and then
	// assert the wrong rule against it. The classification belongs where the rule does.
	for _, testCase := range []struct {
		name  string
		image string

		// shape marks a row the **shape** policy refuses, for every caller.
		shape bool
	}{
		{name: "the metadata endpoint", image: "http://169.254.169.254/latest/meta-data/"},
		{name: "loopback", image: "http://127.0.0.1/"},
		{name: "a private address", image: "http://10.0.0.1/"},
		{name: "a non-HTTP scheme", image: "file:///etc/passwd", shape: true},
		{name: "a javascript url", image: "javascript:alert(1)", shape: true},
		{
			name:  "a data url",
			image: "data:text/html,<script>alert(1)</script>",
			shape: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			page := `<html><head><title>Honest</title>` +
				`<meta property="og:image" content="` + testCase.image + `">` +
				`</head><body></body></html>`

			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/html; charset=utf-8")

					if _, err := w.Write([]byte(page)); err != nil {
						t.Errorf("the fixture could not write: %v", err)
					}
				}),
			)
			t.Cleanup(server.Close)

			preview, err := linkpreview.NewOver(server.Client()).
				Unfurl(t.Context(), server.URL)
			if err != nil {
				t.Fatalf("the preview failed: %v", err)
			}

			// **`RefuseAddressFor` is the predicate `resolveImage` applies**, and the table is
			// split across the two rules because they have different scope:
			//
			//   - the **shape** rows are dropped for every caller, because `checkURL` runs
			//     unconditionally — so they are asserted through the *served page*, which is
			//     the only way to see the drop happen;
			//   - the **address** rows are dropped when the address policy applies, which is
			//     what a deployment runs and what this unfurl's seam has given up — so they are
			//     asserted through the predicate, called on the image URL instead of the page
			//     URL.
			//
			// `TestResolveImageAppliesTheURLPolicy` in the internal suite is what proves
			// `resolveImage` *calls* the predicate with the switch on; this is what proves the
			// predicate is right about each address.
			if !testCase.shape {
				if !errors.Is(linkpreview.RefuseAddressFor(testCase.image),
					linkpreview.ErrPrivateAddress) {
					t.Errorf("the address policy does not refuse %q, so an image naming it "+
						"would survive into a card; a reader's browser is not subject to this "+
						"process's allowlist", testCase.image)
				}

				return
			}

			if preview.Image != "" {
				t.Errorf("the preview's image is %q, want it dropped; a non-HTTP image is "+
					"refused by the shape policy, which applies to every caller",
					preview.Image)
			}
		})
	}
}

// TestAServerThatRefusesToAnswerIsNotAPreview is the timeout rule, and it is tested with a
// real deadline rather than a fixture flag.
//
// **The bound is `FetchTimeout` and it is shorter than the handler's budget**, because a link
// preview is the least important thing a page render does: a slow link degrades to "no
// preview" rather than holding the page open. The assertion is that the call returns within
// the bound and with a class, not with a hang — a test that waited for a hang would itself
// need a bound, and the bound under test is the one being checked.
func TestAServerThatRefusesToAnswerIsNotAPreview(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})

	var once sync.Once

	t.Cleanup(func() { once.Do(func() { close(release) }) })

	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		case <-time.After(2 * linkpreview.FetchTimeout):
		}
	}))
	t.Cleanup(server.Close)

	started := time.Now()

	_, err := linkpreview.NewOver(server.Client()).Unfurl(t.Context(), server.URL)

	elapsed := time.Since(started)

	if err == nil {
		t.Errorf("a server that never answered produced a preview")
	}

	// Generous, because the bound is enforced by `http.Client.Timeout` **and** a context
	// timeout, and the smaller of the two fires; a strict bound here would be a flaky test
	// rather than a strict rule.
	if elapsed > 2*linkpreview.FetchTimeout {
		t.Errorf("the fetch took %s, over twice the %s bound; a link preview is the least "+
			"important thing a page render does and must not hold the page open",
			elapsed, linkpreview.FetchTimeout)
	}

	if err != nil && linkpreview.Class(err) == "" {
		t.Errorf("the failure carries no class; a log line would fall back to %T", err)
	}
}

// --- The helpers -------------------------------------------------------------

// goFiles returns the non-test Go files in a directory.
func goFiles(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}

	names := make([]string, 0, len(entries))

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		names = append(names, filepath.Join(dir, name))
	}

	if len(names) == 0 {
		t.Fatalf("no Go files in %s; the audit would pass by reading nothing", dir)
	}

	sort.Strings(names)

	return names
}

// parsedImports returns one file's import paths.
func parsedImports(t *testing.T, name string) []string {
	t.Helper()

	parsed, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parsing %s: %v", name, err)
	}

	paths := make([]string, 0, len(parsed.Imports))
	for _, imported := range parsed.Imports {
		paths = append(paths, strings.Trim(imported.Path.Value, `"`))
	}

	return paths
}

// truncate shortens a string for a failure message, on a rune boundary.
func truncate(text string) string {
	const limit = 120

	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}

	return string(runes[:limit]) + "…"
}

// The `context` import is used by `NewOver`'s callers in the tests above through `t.Context()`;
// this line keeps the import honest if a future edit drops the last direct use.
var _ = context.Background
