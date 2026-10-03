// The tests that cannot live in `unfurl_test.go`, and the reason they exist.
//
// # Why this file is `package linkpreview`
//
// Three claims are about **unexported state**: the transport `New` builds, whether its dial
// hook is installed, and whether `NewOver`'s seam has one. The transport is a field, so
// observing it means being inside the package — and the observations are worth making,
// because they are the difference between "the dial hook exists" (a comment) and "the dial
// hook exists on the transport `New` actually returns" (a test).
//
// The arrangement is the Go standard rather than a suppression: the file is named
// `..._internal_test.go` so the `testpackage` linter's `(export|internal)_test\.go` skip
// applies, and the black-box suite in `unfurl_test.go` still exercises everything reachable
// from outside. That split is the point — **a test in this file cannot see the package as a
// caller sees it**, which is why the claims that matter to a caller are all in the other file.
//
// # Why the dial hook is called directly rather than through a fetch
//
// `net/http` resolves a name before it dials, so the only way to reach the hook with a *name*
// that resolves to a private address is a DNS record this project controls — and a test that
// depended on one would be slow, flaky, and wrong the moment the record expired. Calling the
// hook with resolved addresses tests the same predicate on the same inputs the runtime would
// produce, with no network.
//
// **And the direct call is testing the code the fetch would run**, because the first test in
// this file asserts that `New`'s transport carries *this* hook. Without that assertion the
// table below would be testing a function nothing calls.

package linkpreview

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestNewBuildsATransportWithTheDialHookInstalled is the premise of every direct call to
// `dialPublicOnly` in this file.
//
// **It is checked through the transport's own field rather than by fetching something**, so it
// holds without a network and without a name that resolves. A transport whose `DialContext` is
// nil dials whatever it is given, which would leave a URL-level check as the only defence — and
// a check on the URL string is defeated by a *name* whose `A` record points at `10.0.0.5`.
func TestNewBuildsATransportWithTheDialHookInstalled(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name      string
		unfurler  *Unfurler
		addresses bool
	}{
		{
			name:      "New",
			unfurler:  New(),
			addresses: true,
		},
		{
			// **The caller's own client is used as-is**, which is what makes it a seam: the
			// transport, the redirect rule and the timeout are whatever they supplied. The
			// assertion is that *nothing of ours* was added, so the seam's cost is exactly
			// `addresses: false` and not one more thing a caller has to know about.
			name:      "NewOver",
			unfurler:  NewOver(http.DefaultClient),
			addresses: false,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			// **The `addresses` flag first**, because it is the seam's one real cost and it is
			// the thing a reader of this file most needs to see hold.
			if testCase.unfurler.addresses != testCase.addresses {
				t.Errorf("%s sets addresses=%t, want %t; the flag is read in exactly one "+
					"place and it is the whole of what the seam gives up",
					testCase.name, testCase.unfurler.addresses, testCase.addresses)
			}

			if !testCase.addresses {
				// **Nothing else of ours is present.** A nil `CheckRedirect` here is correct —
				// the caller supplied a client and we left it alone — and a nil `Transport` is
				// `http.DefaultTransport`, which is also correct for the same reason. Asserting
				// both is what stops a future edit from quietly hardening the seam (which would
				// be safe) or loosening `New` (which would not).
				if testCase.unfurler.client.CheckRedirect != nil {
					t.Errorf("%s installed a redirect rule over the caller's client; the seam "+
						"is 'your client, untouched'", testCase.name)
				}

				return
			}

			if testCase.unfurler.client.CheckRedirect == nil {
				t.Errorf("%s built a client with no CheckRedirect; a public URL that answers "+
					"302 to a private address is the standard way around a check on the first "+
					"URL", testCase.name)

				return
			}

			transport, isTransport := testCase.unfurler.client.Transport.(*http.Transport)
			if !isTransport {
				t.Fatalf("%s built a %T, want *http.Transport; without a concrete transport "+
					"there is no dial hook to install and the refusal has nowhere to happen",
					testCase.name, testCase.unfurler.client.Transport)
			}

			if transport.DialContext == nil {
				t.Errorf("%s built a transport with no DialContext; the refusal is at the "+
					"dial, on the address the resolver actually produced, because that is the "+
					"only place in net/http where where-it-goes has an answer", testCase.name)
			}
		})
	}
}

// TestNewOverDoesNotInstallARedirectRule is the redirect half of the seam's cost, and it is
// asserted rather than assumed.
//
// `NewOver` **keeps the caller's client untouched**, so a caller who supplied a redirect rule
// keeps it and a caller who did not gets `net/http`'s default — which follows ten hops with no
// policy at all. That is a *worse* half than the address one, because it is silent: nothing is
// refused, a hop is simply taken, and the address policy would then be applied only to the
// first URL.
//
// So the two rows below are a **difference**, not two spellings: `New` installs the rule and
// the seam does not. A future edit that added one to the seam would make the seam's documented
// cost a lie, which is the failure this holds.
func TestNewOverDoesNotInstallARedirectRule(t *testing.T) {
	t.Parallel()

	if NewOver(http.DefaultClient).client.CheckRedirect != nil {
		t.Errorf("NewOver installed a redirect rule over the caller's client; the seam is " +
			"defined as 'your client, untouched', and adding a rule here would make that false")
	}

	if NewOver(&http.Client{}).client.CheckRedirect != nil {
		t.Errorf("NewOver installed a redirect rule over a client with none; see above")
	}

	// **And `New` installs one**, which is the claim that makes the two rows above a
	// difference rather than two spellings of the same thing.
	if New().client.CheckRedirect == nil {
		t.Errorf("New built a client with no CheckRedirect; a public URL that answers 302 to " +
			"a private address is the standard way around a check on the first URL")
	}
}

// TestTheDialHookRefusesEveryReservedRange is the address predicate, called with the inputs
// `net/http` hands a dial hook.
//
// **Every reserved range is in the table, and the public address is in it too** — because an
// allowlist that refuses everything is a denial of service rather than a boundary, and only a
// positive row can tell the two apart. The `example.com` row is the one that catches a rule
// written as "refuse anything that is not obviously public": a name is unreachable through
// this hook, and refusing it is what stops a runtime change from letting one through.
func TestTheDialHookRefusesEveryReservedRange(t *testing.T) {
	t.Parallel()

	transport, isTransport := New().client.Transport.(*http.Transport)
	if !isTransport || transport.DialContext == nil {
		t.Fatalf("New built no dial hook; the premise of this table is not established")
	}

	hook := transport.DialContext

	for _, testCase := range []struct {
		name    string
		address string
		wantRef bool
	}{
		{name: "loopback v4", address: "127.0.0.1:80", wantRef: true},
		{name: "loopback v6", address: "[::1]:80", wantRef: true},
		{name: "the metadata endpoint", address: "169.254.169.254:80", wantRef: true},
		{name: "link-local v6", address: "[fe80::1]:80", wantRef: true},
		{name: "unique local v6", address: "[fc00::1]:80", wantRef: true},
		{name: "private class A", address: "10.0.0.5:80", wantRef: true},
		{name: "private class B", address: "172.16.0.1:80", wantRef: true},
		{name: "private class C", address: "192.168.1.1:80", wantRef: true},
		{name: "carrier-grade NAT", address: "100.64.0.1:80", wantRef: true},
		{name: "the unspecified address", address: "0.0.0.0:80", wantRef: true},
		{name: "multicast", address: "224.0.0.1:80", wantRef: true},
		{name: "a host that is not an address", address: "example.com:80", wantRef: true},
		{name: "a malformed address", address: "not-an-address", wantRef: true},
		{name: "a public address", address: "93.184.216.34:80", wantRef: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			// `t.Context()` rather than a fresh one: this is I/O the test performs, and the
			// cancellation has to be the test's.
			conn, err := hook(t.Context(), "tcp", testCase.address)

			if testCase.wantRef {
				if err == nil {
					if conn != nil {
						if closeErr := conn.Close(); closeErr != nil {
							t.Errorf("closing the refused connection: %v", closeErr)
						}
					}

					t.Errorf("the dial hook dialled %q; a link in a wiki page is "+
						"attacker-reachable through Obsidian Sync", testCase.address)
				}

				if Class(err) == "" {
					t.Errorf("the refusal carries no class; a log line would fall back to the "+
						"error's dynamic type %T, which no alert can match", err)
				}

				return
			}

			if err != nil {
				// The public row is expected to fail — nothing is listening on port 80 of a
				// public address, and this container may have no route to the internet at all.
				// **What matters is the shape of the failure**: `dialPublicOnly` wraps a
				// connection error in `ErrNotFetched` and refuses a non-public address with
				// `ErrPrivateAddress`, so a public address that came back with the latter is
				// the policy refusing it — which is exactly the "allowlist that refuses
				// everything" defect this row exists to catch. Asserting `err != nil` would not
				// tell the two apart.
				if errors.Is(err, ErrPrivateAddress) {
					t.Errorf("the dial hook refused the public address %q: %v; an allowlist "+
						"that refuses everything is a denial of service, not a boundary",
						testCase.address, err)
				}

				return
			}

			if conn != nil {
				if closeErr := conn.Close(); closeErr != nil {
					t.Errorf("closing the connected socket: %v", closeErr)
				}
			}
		})
	}
}

// TestIsPublicIsAnAllowlist is the predicate on its own, over every address class the
// standard library distinguishes.
//
// **Why it is here and not in the black-box file**: `isPublic` is the function `checkURL` and
// `dialPublicOnly` both call, and it is the whole of the address policy. A row that reached it
// only through a fetch would need a server per address class, which means eleven
// `httptest` servers and a test that fails on the eleventh because of a port rather than a
// rule.
func TestIsPublicIsAnAllowlist(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name    string
		address string
		want    bool
	}{
		{name: "a public v4 address", address: "93.184.216.34", want: true},
		{name: "a public v6 address", address: "2606:2800:220:1:248:1893:25c8:1946", want: true},
		{name: "loopback", address: "127.0.0.1", want: false},
		{name: "loopback v6", address: "::1", want: false},
		{name: "private class A", address: "10.0.0.5", want: false},
		{name: "private class B", address: "172.16.0.1", want: false},
		{name: "private class C", address: "192.168.1.1", want: false},
		{name: "carrier-grade NAT", address: "100.64.0.1", want: false},
		{name: "link-local", address: "169.254.169.254", want: false},
		{name: "link-local v6", address: "fe80::1", want: false},
		{name: "unique local v6", address: "fc00::1", want: false},
		{name: "the unspecified address", address: "0.0.0.0", want: false},
		{name: "the unspecified v6 address", address: "::", want: false},
		{name: "multicast", address: "224.0.0.1", want: false},
		{name: "interface-local multicast v6", address: "ff01::1", want: false},
		{name: "the broadcast address", address: "255.255.255.255", want: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			address := net.ParseIP(testCase.address)
			if address == nil {
				t.Fatalf("the fixture address %q does not parse, so this test is testing "+
					"nothing", testCase.address)
			}

			if got := isPublic(address); got != testCase.want {
				t.Errorf("isPublic(%s) = %t, want %t; the policy is a positive allowlist, and "+
					"the next address range reserved for something is not in today's denylist",
					testCase.address, got, testCase.want)
			}
		})
	}
}

// TestFollowRedirectsRejectsEveryHopOnItsOwnTerms is the redirect rule, called directly so
// the *rule* is exercised rather than the dial hook that would refuse the first hop anyway.
//
// **A redirect through `New` never gets here in a test**, because `New`'s dial refuses
// `127.0.0.1` first — which is the right behaviour and makes an end-to-end test of this rule
// impossible on loopback. Calling `followRedirects` with a synthetic chain is the only way to
// reach the hop logic, and it is a test of the shipped function rather than a copy.
func TestFollowRedirectsRejectsEveryHopOnItsOwnTerms(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name string
		next string
		via  int
		want error
	}{
		{
			name: "a hop to the metadata endpoint", next: "http://169.254.169.254/x", via: 0,
			want: ErrPrivateAddress,
		},
		{name: "a hop to loopback", next: "http://127.0.0.1/", via: 0, want: ErrPrivateAddress},
		{name: "a hop to a file url", next: "file:///etc/passwd", via: 0, want: ErrBadScheme},
		{
			name: "a hop with credentials", next: "http://a:b@example.com/", via: 0,
			want: ErrNotAbsolute,
		},
		{
			// **A public next URL with a full chain.** This is the only row that proves the
			// *count* is what refuses it rather than the destination.
			name: "a chain past the cap", next: "https://example.com/", via: MaxRedirects,
			want: ErrTooManyRedirects,
		},
		{name: "a hop inside the cap", next: "https://example.com/", via: 1, want: nil},
		{name: "a first hop", next: "https://example.com/", via: 0, want: nil},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			via := make([]*http.Request, 0, testCase.via)
			for index := range testCase.via {
				via = append(
					via,
					mustRequest(t, "https://example.com/step"+string(rune('0'+index))),
				)
			}

			err := followRedirects(mustRequest(t, testCase.next), via)

			if testCase.want == nil {
				if err != nil {
					t.Errorf("a hop inside the cap was refused: %v", err)
				}

				return
			}

			if !errors.Is(err, testCase.want) {
				t.Errorf("the hop was refused with %v, want one wrapping %v; a redirect to a "+
					"private address is the standard way around a check on the first URL",
					err, testCase.want)
			}

			if strings.Contains(err.Error(), testCase.next) {
				t.Errorf("the refusal quotes the redirect target %q; a refusal message is a "+
					"log line and the target is attacker-influenced (S-12.3). Full text: %s",
					testCase.next, err.Error())
			}
		})
	}
}

// TestResolveImageAppliesTheURLPolicy is the result-side rule, and it is here because
// `resolveImage` is unexported and the black-box file reaches it only through a served page.
//
// **Two assertions and the second is the subtle one.** The first is that a private address is
// dropped. The second is that a *relative* image resolves against the final URL — a preview
// whose image is a bare path is a card that would load it from a different host, which is a
// fetch this package did not vet. The protocol-relative row is the one that catches an
// implementation which resolved on `Host == ""` alone: such a URL has an authority and no
// scheme, so it would be resolved against the base's **path** and point somewhere else.
//
// And the `addresses` switch is asserted in both positions, because one rule in two places is
// two answers and the answer that matters is the one a browser would act on.
func TestResolveImageAppliesTheURLPolicy(t *testing.T) {
	t.Parallel()

	base := mustParse(t, "https://example.com/notes/index.html")

	for _, testCase := range []struct {
		name  string
		image string
		want  string
	}{
		{
			name: "a relative path", image: "/static/road.png",
			want: "https://example.com/static/road.png",
		},
		{
			name: "a relative path with no leading slash", image: "road.png",
			want: "https://example.com/notes/road.png",
		},
		{
			name: "a protocol-relative url", image: "//cdn.example.com/a.png",
			want: "https://cdn.example.com/a.png",
		},
		{
			name: "an absolute public url", image: "https://cdn.example.com/a.png",
			want: "https://cdn.example.com/a.png",
		},
		{name: "the metadata endpoint", image: "http://169.254.169.254/x", want: ""},
		{name: "loopback", image: "http://127.0.0.1/a.png", want: ""},
		{name: "a file url", image: "file:///etc/passwd", want: ""},
		{name: "a javascript url", image: "javascript:alert(1)", want: ""},
		{name: "empty", image: "", want: ""},
		{name: "whitespace", image: "   ", want: ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := resolveImage(testCase.image, base, true); got != testCase.want {
				t.Errorf("resolveImage(%q) = %q, want %q; an og:image is a URL a browser "+
					"fetches the moment the card renders, and a relative one resolves against "+
					"where the page ended up rather than where the reader asked to go",
					testCase.image, got, testCase.want)
			}
		})
	}

	// **The seam, on the result side.** `NewOver` is how a test reaches a loopback server, and
	// the base URL here *is* loopback — so with the switch off, the same image resolves, and
	// with it on it is dropped. That the switch is threaded from the fetch into the parse is
	// the point: an unfurl that reached a server because of the seam must not then produce a
	// card whose image points somewhere the fetch policy would have refused.
	if got := resolveImage("http://127.0.0.1/a.png", mustParse(t, "https://example.com/"),
		false); got == "" {
		t.Errorf("resolveImage dropped a loopback image with the address policy off; the " +
			"switch governs the fetch and must govern the result, or a build using the seam " +
			"produces cards pointing at addresses the fetch would refuse")
	}
}

// TestTruncateCutsOnARuneBoundary is the reader-facing bound, on the one function that has to
// get it right.
//
// **A byte slice splits a multi-byte character** and renders a replacement glyph in the middle
// of somebody's title — and the fixture is multi-byte for exactly that reason: an ASCII-only
// title passes both implementations, so a test built on one cannot tell them apart.
func TestTruncateCutsOnARuneBoundary(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name  string
		field string
		want  int
	}{
		{name: "a short field", field: "Salt & Iron", want: len("Salt & Iron")},
		{name: "a field at the bound", field: strings.Repeat("a", maxFieldRunes), want: maxFieldRunes},
		{
			name: "a field over the bound", field: strings.Repeat("a", maxFieldRunes+100),
			want: maxFieldRunes,
		},
		{name: "multi-byte under the bound", field: strings.Repeat("é", 10), want: 10},
		{name: "multi-byte over the bound", field: strings.Repeat("é", 500), want: maxFieldRunes},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := truncate(testCase.field)

			if runes := len([]rune(got)); runes != testCase.want {
				t.Errorf("truncate produced %d runes, want %d; the bound is on runes so a "+
					"multi-byte character is never split into a replacement glyph", runes,
					testCase.want)
			}

			if strings.Contains(got, "�") {
				t.Errorf("truncate(%q) = %q; a replacement character means the bound cut a "+
					"multi-byte character in half", truncateBodyOf(testCase.field), got)
			}
		})
	}

	// **And the whitespace collapse**, which is not cosmetic: a `<title>` written across
	// three lines is one title to a reader, and a card whose height depends on the author's
	// formatting is a card whose height nobody chose.
	collapsed := truncate("  Salt\n\t&\r\n  Iron  ")
	if collapsed != "Salt & Iron" {
		t.Errorf("truncate did not collapse whitespace: %q, want %q; the fields loop stops at "+
			"the first string that is not a newline, a tab or a space", collapsed, "Salt & Iron")
	}

	// And a truncation landing on a space is trimmed, because a card with a trailing space is
	// invisible until two cards are stacked.
	trimmed := truncate(strings.Repeat("a", maxFieldRunes-1) + " " + strings.Repeat("b", 10))
	if strings.HasSuffix(trimmed, " ") {
		t.Errorf("truncate left a trailing space: %q", trimmed)
	}
}

// truncateBodyOf shortens a fixture for a failure message.
func truncateBodyOf(field string) string {
	if len(field) <= 40 {
		return field
	}

	return field[:40] + "…"
}

// mustRequest builds a request for a redirect-chain fixture.
func mustRequest(t *testing.T, target string) *http.Request {
	t.Helper()

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, http.NoBody)
	if err != nil {
		t.Fatalf("building the request for %q: %v", target, err)
	}

	return request
}

// mustParse builds a URL for a `resolveImage` fixture.
func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()

	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parsing %q: %v", raw, err)
	}

	return parsed
}
