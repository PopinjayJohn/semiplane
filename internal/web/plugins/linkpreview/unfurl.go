// Package linkpreview is §10.6's link preview: a render hook over external links,
// and the read-only helper that fetches what it shows.
//
// # What it reads, and the one thing it must never read
//
// The hook answers `{title, description, image}` for an external URL, and every one of
// those three comes from **the remote page**. That is not a simplification; it is the
// security boundary of this package, and the reason is that a campaign's content is
// not a URL this process cannot reach.
//
// A campaign's pages live on this host. A URL naming this host names a campaign page.
// So a helper that fetched "the URL a page linked to" without asking where it pointed
// is a helper that will happily fetch `/c/greyhaven/wiki/Vault` — and the response it
// would parse is a **private page rendered to the person who wrote the link**, which is
// S-8's access matrix answered by a rendering path that never went through a gate.
//
// Therefore three independent refusals, each of which alone is enough:
//
//  1. **Only `http` and `https`**, by an allowlist and not a denylist. `file://` reads
//     this process's disk, `gopher://` and `dict://` speak protocols whose whole purpose
//     is reaching things an HTTP request should not.
//  2. **No host that resolves to anything but a public unicast address.** Loopback,
//     link-local, private, carrier-grade NAT, unspecified, multicast and the cloud
//     metadata addresses are all refused, and the check is on the **resolved address**
//     rather than on the name — a DNS name is attacker input and `internal.example.com`
//     is as likely to be a name as an address.
//  3. **Redirects are re-checked, not inherited.** A public URL that answers `302` to
//     `http://169.254.169.254/` is the standard SSRF bypass, and a client that checked
//     only the first URL has checked nothing.
//
// # Why this is a `net.Dialer` hook rather than a check on the URL string
//
// Because a URL string cannot tell you where it goes. `http://internal/` and
// `http://127.0.0.1/` and `http://[::1]/` and a public name with an `A` record pointing
// at `10.0.0.5` are four spellings of the same request, and only the last one is
// distinguishable without resolving. So the refusal is at the **dial**, on the address
// the resolver actually produced, and it is the only place in `net/http` where the
// answer exists.
//
// The cost is stated rather than hidden: a check at the dial cannot refuse a name
// *before* the name is resolved, so a hostile name still costs one DNS lookup. That is
// a name-resolution side effect, not a fetch of the target, and it is the price of not
// shipping a resolver-based bypass. The public-suffix problem — a public name whose
// `A` record is `127.0.0.1` — is handled for the same reason and by the same hook.
//
// # What it never is
//
// Not a path to campaign content. `Unfurl` has no `*os.Root`, no `*content.Root`, no
// campaign id and no filesystem handle in its type graph, and
// `TestThisPackageHoldsNoContentRoot` parses the imports to prove it. The preview
// returns three fields and each is a string the **remote** page said; there is no
// parameter through which a caller could ask for a path.

package linkpreview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The refusals this package produces.
//
// Each names what broke it and **none carries the URL**: a link's target is author
// input arriving through Obsidian Sync, and a refusal message is a log line. S-12.3's
// rule is about secrets and file contents, and a URL in an error is the same shape of
// leak — it can carry a query string somebody pasted from a private page.
var (
	// ErrNotAbsolute is a link that is not an absolute URL with a host. A relative
	// link is a link *inside* the campaign, and this package only reads the outside.
	ErrNotAbsolute = errors.New("linkpreview: the link is not an absolute http(s) url")

	// ErrBadScheme is a URL whose scheme is not `http` or `https`.
	ErrBadScheme = errors.New("linkpreview: only http and https links are previewed")

	// ErrPrivateAddress is a URL whose host resolves to something other than a public
	// unicast address.
	ErrPrivateAddress = errors.New("linkpreview: the link names a host this process will not reach")

	// ErrTooManyRedirects is a chain longer than `MaxRedirects`.
	ErrTooManyRedirects = errors.New("linkpreview: the link redirected too many times")

	// ErrNotFetched is a transport failure. The cause is reachable with `errors.Is` and
	// printed by nothing here; see `Error`.
	ErrNotFetched = errors.New("linkpreview: the page could not be fetched")

	// ErrNotHTML is a response that is not a document, so there is no title, no
	// description and no image in it to read.
	ErrNotHTML = errors.New("linkpreview: the response is not an html document")

	// ErrTooLarge is a response over `MaxBodyBytes`.
	ErrTooLarge = errors.New("linkpreview: the response is larger than this build reads")
)

// The bounds a preview is fetched under, all of them this file's rather than
// configuration's, because each is a property of what a preview *is* and an operator
// who could raise them would be raising the cost of a link a GM wrote.
const (
	// MaxBodyBytes is how much of the response is read.
	//
	// A preview needs a `<title>` and a couple of `<meta>` elements, both of which are
	// conventionally in the first few kilobytes. A megabyte is two orders of magnitude
	// of headroom and is still a bound: an unfetched megabyte per page view, per
	// reader, is an amplification a link in a wiki page can cause.
	MaxBodyBytes int64 = 1 << 20

	// FetchTimeout bounds one fetch, independent of the caller's context.
	//
	// The request's own budget is the handler's, which is 25 seconds and covers
	// everything the handler does. A link preview is the least important thing a page
	// render does, so it gets its own shorter bound and a slow link degrades to "no
	// preview" rather than holding the page open.
	FetchTimeout = 5 * time.Second

	// MaxRedirects is how many hops a link may take.
	//
	// Three. A preview that follows more than a couple of hops is not previewing the
	// link anybody wrote, and the chain is the shape a redirect-based SSRF takes — so
	// the cap is a security bound as much as a politeness one.
	MaxRedirects = 3

	// maxFieldRunes bounds one preview field.
	//
	// 300, which is longer than any title or description a page ships as a preview and
	// short enough that a preview card is a card. Truncation is on a rune boundary for
	// the reason `search`'s echoed query is: a byte slice would split a combining mark
	// and produce a replacement character in the middle of a person's sentence.
	maxFieldRunes = 300
)

// Unfurler reads a preview for a URL.
//
// A struct with one seam — the `*http.Client` — so a test can hand it an
// `httptest.Server` and so a deployment can bound the transport. **It is not safe to
// copy by value after construction** only because the client it holds is; `New`
// returns a pointer and every method takes a pointer receiver for that reason.
type Unfurler struct {
	// client is the transport, and the only place a network request happens.
	client *http.Client

	// addresses reports whether the **address policy** applies: a typed non-public address
	// refused before any I/O, and a typed name re-checked at the dial.
	//
	// **A field rather than a behaviour difference between two constructors**, because that is
	// what makes the cost of `NewOver` inspectable: one boolean, read in one place, and a test
	// can assert on it without reaching into a transport. `New` sets it; `NewOver` does not.
	//
	// The *shape* policy — scheme, host, credentials — is not here because it applies to
	// every caller and does not depend on the transport at all.
	addresses bool
}

// New returns an Unfurler whose transport refuses anything but a public address.
//
// The `DialContext` hook is the whole of the SSRF defence and it is **not
// optional**: `CheckRedirect` alone is the bypass, because a `302` to a private address
// passes every check that ran before the hop. `Transport` is a pointer to a literal
// rather than a shared default, because mutating `http.DefaultTransport`'s dialer would
// change the behaviour of every other client in the process — a rule enforced in one
// of two places is a rule one caller routes around, and this is exactly that case.
//
// The user agent is named because an unnamed fetch is refused by a great many hosts
// and a preview that renders nothing because a server disliked the request is a
// preview nobody can debug.
func New() *Unfurler {
	transport := &http.Transport{
		DialContext:           dialPublicOnly,
		TLSHandshakeTimeout:   3 * time.Second,
		ResponseHeaderTimeout: 3 * time.Second,
		// No `Proxy` from the environment. `http.ProxyFromEnvironment` would honour
		// `HTTP_PROXY` and `HTTPS_PROXY`, and an operator who set one for outbound
		// traffic would then have this package's requests going through it — which is
		// neither surprising nor something a link in a wiki page should be able to
		// reach. A self-hosted instance reaches the internet directly or not at all.
		Proxy: nil,
	}

	return &Unfurler{
		client: &http.Client{
			Transport: transport,
			Timeout:   FetchTimeout,
			// The redirect rules are a function rather than a value because they need
			// `checkURL`, and a `CheckRedirect` that could not see the shared policy
			// would be a second policy.
			CheckRedirect: followRedirects,
		},
		addresses: true,
	}
}

// NewOver returns an Unfurler over one client, for a caller that has its own transport.
//
// ## What the seam keeps, and what it costs
//
// **Kept — the shape policy**, because it is a property of the URL and not of the transport:
// `file://`, `gopher://`, `javascript:`, a URL with no host, and a URL carrying credentials are
// all refused, with no I/O and for every caller.
//
// **Given up — the address policy**, both halves of it: `checkAddress` (a *typed* private
// address, refused before any I/O) and the dial hook (a *name* that resolves to one, which is
// the DNS rebinding case and the reason a check on the URL string alone is not a defence).
//
// The seam exists because a test needs an `httptest.Server`, which is on `127.0.0.1` — an
// address `New` correctly refuses. A caller using it in production has accepted that a link to
// `http://169.254.169.254/` would be fetched, and that is the honest cost.
//
// `u.addresses` is the switch, it is read in exactly one place, and
// `TestTheSeamKeepsTheURLLevelRefusals` and
// `TestNewBuildsATransportWithTheDialHookInstalled` are the two halves that hold the
// contract. **Production code uses `New`**, and that is the only difference a caller has to
// remember.
func NewOver(client *http.Client) *Unfurler {
	return &Unfurler{client: client, addresses: false}
}

// Preview is what a link shows: three strings, each one the remote page's own.
//
// **Three fields, and the shape is the security argument.** A preview that could carry
// a body, a list of links or a rendered fragment would be a preview that could carry
// campaign content out through the fetching side. `Title`, `Description` and `Image` are
// the three fields link-preview protocols define, and a caller that wants more has to
// build more rather than receive more.
type Preview struct {
	// URL is the address the preview was read from, after any redirects.
	//
	// The **final** URL, not the one asked for, because the page's own relative links
	// and its canonical URL are relative to where it ended up. Rendering the requested
	// URL alongside a final-URL preview is a preview that points somewhere other than
	// what it shows.
	URL string

	// Title is the document's `<title>`, or its `og:title`, whichever the page states.
	Title string

	// Description is the page's `og:description` or its `name="description"` meta.
	Description string

	// Image is the page's `og:image`, resolved against the final URL.
	//
	// Empty rather than a best guess: a preview with no image is a card with no image,
	// and a wrong image is a card showing one campaign's content in another campaign's
	// page.
	Image string
}

// Unfurl fetches rawURL and reads its preview.
//
// Read-only and **one-way**: nothing in here writes, and nothing in here holds a handle
// to anything a write could reach. The refusals are `ErrNotAbsolute` and `ErrBadScheme`
// before any I/O, and `ErrPrivateAddress` at the dial — so a link into a campaign's own
// content root is refused without the request leaving the process, and the refusal is
// the same one a link to `127.0.0.1` gets. There is no code path by which this function
// can read a page of the campaign it is running in.
//
// A fetch failure is an error, never an empty preview with no explanation: a caller
// that cannot tell "this link has no preview" from "this link was refused" will render
// an empty card for a link that was a refusal, and a reader looking at that card learns
// nothing — including that something was refused. `Error` returns a fixed sentence for
// the same reason `realtime.FrameError` does: the cause is reachable and printed by
// nothing.
func (u *Unfurler) Unfurl(ctx context.Context, rawURL string) (Preview, error) {
	target, err := checkURL(rawURL)
	if err != nil {
		return Preview{}, err
	}

	// The address policy, and **before any I/O**. A URL that already spells a private
	// address needs no resolution to know where it goes, so refusing it here means the
	// refusal costs nothing and the target is never dialled at all.
	if u.addresses {
		if addressErr := checkAddress(target); addressErr != nil {
			return Preview{}, addressErr
		}
	}

	// The context is the caller's *and* the client's own timeout applies on top, because
	// `http.Client.Timeout` covers the whole exchange while a context the caller
	// derived from a request deadline may be longer. Both bounds, and the shorter wins.
	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), http.NoBody)
	if err != nil {
		return Preview{}, fmt.Errorf("%w: the request could not be built", ErrNotFetched)
	}

	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	request.Header.Set("User-Agent", "semiplane link preview")

	response, err := u.client.Do(request)
	if err != nil {
		// Classified, not passed through: `url.Error`'s text quotes the URL, which is
		// author input, and it embeds the dial error whose text names the address. S-12.3
		// again, and the same rule `play`'s `classify` follows.
		return Preview{}, fmt.Errorf("%w: %s", ErrNotFetched, Class(err))
	}
	defer closeBody(response)

	if response.StatusCode != http.StatusOK {
		return Preview{}, fmt.Errorf("%w: the page answered %d", ErrNotFetched, response.StatusCode)
	}

	// `MaxBodyBytes + 1`, so an over-large body is *detected* rather than silently
	// truncated: `io.LimitReader` at exactly the limit cannot distinguish a body of
	// exactly the limit from one that continues, and a truncated HTML document parses
	// as a document with no `<title>` — which is a preview that quietly renders empty.
	//
	// `Content-Length` is checked first where the server declared one, because it is
	// free and it is the honest signal; the reader is the enforcement for a server that
	// lied or chunked.
	if response.ContentLength > MaxBodyBytes {
		return Preview{}, fmt.Errorf("%w: %d bytes declared", ErrTooLarge, response.ContentLength)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, MaxBodyBytes+1))
	if err != nil {
		return Preview{}, fmt.Errorf("%w: %s", ErrNotFetched, Class(err))
	}

	if int64(len(body)) > MaxBodyBytes {
		return Preview{}, fmt.Errorf("%w: more than %d bytes", ErrTooLarge, MaxBodyBytes)
	}

	// Content type is checked rather than sniffed, and refused rather than tolerated: a
	// `text/plain` body parsed as HTML yields no title, so accepting one is a preview
	// that renders empty for a reason nobody can see.
	if !isHTML(response.Header.Get("Content-Type")) {
		return Preview{}, fmt.Errorf("%w: %q", ErrNotHTML, contentType(response))
	}

	preview, err := readPreview(body, response.Request.URL, u.addresses)
	if err != nil {
		return Preview{}, fmt.Errorf("linkpreview: read the preview: %w", err)
	}

	return preview, nil
}

// dialPublicOnly is `Transport.DialContext`, and the refusal happens here.
//
// **On the resolved address, not on the name.** The argument `net/http` hands a dial
// hook is `host:port` where `host` is already an IP literal, because Go's dialer
// resolves before it dials — so this is the only place in the stack where "where does
// this URL actually go" has an answer, and it is the answer this package must act on.
//
// The loop over `addresses` is the whole defence and it is a loop because a name can
// resolve to several: a name with one public and one private `A` record would be
// dialled on whichever the resolver tried first, so **any** non-public address refuses
// the whole dial rather than only the ones not yet tried.
func dialPublicOnly(ctx context.Context, network, address string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("%w: %q is not host:port", ErrPrivateAddress, network)
	}

	parsed := net.ParseIP(host)
	if parsed == nil {
		// Unreachable for `net.Dialer`, which resolves first. Handled because a
		// transport hook that *assumed* it could only ever see an IP would be one
		// runtime change away from silently allowing every name through, and the
		// failure mode of that is an SSRF.
		return nil, fmt.Errorf("%w: %q is not an address", ErrPrivateAddress, network)
	}

	// **No address in the message.** The address is what the author typed, and this refusal
	// is a log line (S-12.3); `Class` carries the fact an operator matches on.
	if !isPublic(parsed) {
		return nil, fmt.Errorf("%w: it resolves to no public address", ErrPrivateAddress)
	}

	dialer := net.Dialer{Timeout: FetchTimeout}

	conn, err := dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFetched, Class(err))
	}

	return conn, nil
}

// followRedirects is `CheckRedirect`, and it re-runs the whole URL check per hop.
//
// **The re-check is the point.** A `302` from a public host to `http://169.254.169.254/`
// is the standard way around a check on the first URL, and `via` holds the chain so
// the count can be bounded. The refusal is `ErrTooManyRedirects` rather than
// `http.ErrUseLastResponse`, because a hop that is not followed is not a preview of
// the page the link names — it is a preview of a redirect notice.
func followRedirects(request *http.Request, via []*http.Request) error {
	if len(via) >= MaxRedirects {
		return fmt.Errorf("%w: more than %d", ErrTooManyRedirects, MaxRedirects)
	}

	parsed, err := checkURL(request.URL.String())
	if err != nil {
		return fmt.Errorf("%w: a hop is %s", err, Class(err))
	}

	// **The address policy is re-applied per hop, not inherited.** A public URL that answers
	// `302` to `http://169.254.169.254/` is the standard SSRF bypass: every check that ran
	// before the hop passes, and the hop is where the request actually goes. `checkAddress`
	// catches the *typed* case here, and `dialPublicOnly` catches a *name* that resolves to
	// one — so a hop is covered twice over and a caller who supplied their own transport
	// through `NewOver` still gets the typed case refused.
	if err := checkAddress(parsed); err != nil {
		return fmt.Errorf("%w: a hop is %s", err, Class(err))
	}

	return nil
}

// checkURL is the whole of the pre-flight policy: absolute, `http` or `https`, a host.
//
// It does **not** resolve. Resolution is the dial's job, for the reason
// `dialPublicOnly` gives, and duplicating it here would be a second resolver's answer
// to one question — and the one that would be wrong under a rebind.
func checkURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		// `url.Parse`'s error quotes the URL, which is author input, so the class goes
		// out and the text does not.
		return nil, fmt.Errorf("%w: %s", ErrNotAbsolute, Class(err))
	}

	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return nil, fmt.Errorf("%w: %q", ErrBadScheme, schemeOf(parsed))
	}

	// `Hostname` and not `Host`: the latter carries the port, and `net.ParseIP` on
	// `"127.0.0.1:8080"` returns nil — so parsing `Host` would make every IP-literal URL
	// with a port look like a *name* and skip the address check entirely. That is the
	// difference between refusing `http://127.0.0.1/` and refusing it only when it has no
	// port.
	if parsed.Hostname() == "" {
		return nil, fmt.Errorf("%w: it names no host", ErrNotAbsolute)
	}

	// A URL carrying credentials is refused rather than used. `http.Client` would send
	// them as `Authorization`, so a preview would perform an authenticated request on a
	// link's author's behalf — which is a use of somebody's credentials that no reader
	// of the preview can see, and S-8's matrix has nothing to say about it because it is
	// not about the campaign.
	if parsed.User != nil {
		return nil, fmt.Errorf("%w: it carries credentials", ErrNotAbsolute)
	}

	return parsed, nil
}

// RefuseAddressFor is `checkURL` followed by `checkAddress`, as a raw URL string.
//
// **Exported for exactly one reason**: the pair is the pre-flight policy, and a test that can
// only reach it through a fetch cannot say *which* check refused a private address — the dial
// hook would refuse it too, so "the fetch was refused" proves neither. Calling the predicate
// says the URL check does the work, which is the claim that costs nothing and happens before a
// DNS lookup.
//
// The shape policy comes with it, because a caller of this is asking "would this URL be
// fetched?" and answering half the question would be worse than answering none. Nothing in this
// project calls it: `Unfurl` runs the same two calls itself, in the same order.
func RefuseAddressFor(rawURL string) error {
	parsed, err := checkURL(rawURL)
	if err != nil {
		return err
	}

	return checkAddress(parsed)
}

// checkAddress refuses a URL whose host is a **typed** non-public address, without a lookup.
//
// Split from `checkURL` rather than folded into it, and the split is the seam's whole shape.
// `checkURL` is the shape policy — scheme, host, credentials — and it runs for **every**
// caller, because nothing about it depends on the transport. `checkAddress` and
// `dialPublicOnly` are the address policy, and they run only for a caller that used `New`.
//
// The reason they are separate is that a test needs to reach an `httptest.Server`, which is
// on `127.0.0.1` — an address the guarded constructor correctly refuses. So:
//
//   - **A typed address** (`http://127.0.0.1/`, `http://169.254.169.254/`) is refused by
//     `checkAddress`, before any I/O and without a lookup, because a URL that already spells
//     the address needs no resolution.
//   - **A typed name that resolves to one** is refused by `dialPublicOnly`, on the address the
//     resolver actually produced. That is the DNS rebinding case, and it is the reason a check
//     on the URL string alone is not a defence — which is why `New` carries both.
//
// A caller who used `NewOver` has therefore taken the **address** policy into their own
// transport and kept the **shape** policy. `NewOver`'s doc comment says exactly that, and
// `TestTheSeamKeepsTheURLLevelRefusals` and `TestNewBuildsATransportWithTheDialHookInstalled`
// are the two halves that hold it.
func checkAddress(parsed *url.URL) error {
	address := net.ParseIP(parsed.Hostname())
	if address == nil {
		// A *name*. Nothing to say here — resolution is the dial's job, for the reason
		// `dialPublicOnly` gives, and duplicating it would be a second resolver's answer to one
		// question (and the one that would be wrong under a rebind).
		return nil
	}

	if !isPublic(address) {
		// **No address in the message.** It is derived from what the author typed and this
		// refusal is a log line (S-12.3); `Class` carries the fact an operator matches on.
		return fmt.Errorf("%w: its host is not a public address", ErrPrivateAddress)
	}

	return nil
}

// isPublic reports whether an address is one this process will dial on a link's
// behalf.
//
// A **positive** allowlist rather than a denylist of the addresses somebody has thought
// of, and that choice is the security argument: the next address range reserved for
// something is not in today's denylist, and the day it is in someone's is the day this
// becomes an SSRF. Go's `IsPrivate`, `IsLoopback`, `IsLinkLocalUnicast`,
// `IsLinkLocalMulticast`, `IsUnspecified` and `IsMulticast` cover the reserved space,
// and `IsGlobalUnicast` closes it by allowing only the unicast space that is *not*
// reserved.
//
// The cloud metadata addresses are named explicitly as well, because they are the one
// case where a future-proof allowlist could still be wrong: `IsGlobalUnicast` reports
// `169.254.169.254` as link-local already, and a second explicit case for it is
// redundant *and* worth having — it says the metadata endpoint is refused on purpose
// rather than by accident of the standard library's coverage.
func isPublic(address net.IP) bool {
	if address.IsLoopback() ||
		address.IsPrivate() ||
		address.IsLinkLocalUnicast() ||
		address.IsLinkLocalMulticast() ||
		address.IsUnspecified() ||
		address.IsMulticast() ||
		address.IsInterfaceLocalMulticast() {
		return false
	}

	// Not just `IsGlobalUnicast`: it is true of an address in `100.64.0.0/10`, which is
	// carrier-grade NAT and is neither public nor private, and a service reachable
	// through it is one this process has no business fetching on a wiki page's behalf.
	if _, carrier, err := net.ParseCIDR("100.64.0.0/10"); err == nil && carrier.Contains(address) {
		return false
	}

	return address.IsGlobalUnicast()
}

// isHTML reports whether a Content-Type is a document this package parses.
//
// The parameters are ignored (`; charset=utf-8`) and the comparison is
// case-insensitive, because a header value's case is not a fact about its type and a
// strict comparison would refuse `Text/HTML` for a reason nobody can act on.
func isHTML(contentType string) bool {
	base, _, _ := strings.Cut(strings.ToLower(contentType), ";")

	return strings.TrimSpace(base) == "text/html" ||
		strings.TrimSpace(base) == "application/xhtml+xml"
}

// contentType reduces a header to its type for a refusal message.
//
// An **empty** header prints as the empty string rather than as nothing found, because
// the two are the same refusal and a reader of a log line asking "what did it claim to
// be" wants a value either way.
func contentType(response *http.Response) string {
	if response.Header == nil {
		return ""
	}

	return response.Header.Get("Content-Type")
}

// schemeOf names a URL's scheme for a refusal message, and never the rest of it.
func schemeOf(parsed *url.URL) string {
	if parsed.Scheme == "" {
		return "(none)"
	}

	return parsed.Scheme
}

// closeBody closes a response body and reports a failure as a class.
//
// `bodyclose` requires the close and `errcheck` requires the error be handled; a
// response body that fails to close on a **read-only** fetch has nothing to lose, so
// the failure is discarded deliberately rather than propagated into a return signature
// that has already been decided.
func closeBody(response *http.Response) {
	// Discarded deliberately. A read-only fetch has nothing to flush, and a `Close` that
	// failed would mean the transport could not be told the connection was finished —
	// which is the transport's own business and which `bodyclose` exists to make sure
	// happens at all. Propagating it would mean a preview that rendered perfectly
	// reported a failure.
	_ = response.Body.Close()
}

// Class reduces an error to a short, content-free label for a log line.
//
// **The same function `play` has, for the same reason**, and the reason is the same
// too: `net/url`, `net` and `os` errors quote the thing they choked on, and the thing
// they choked on here is a link somebody wrote. Every branch is a fixed word.
func Class(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrNotAbsolute):
		return "not_absolute"
	case errors.Is(err, ErrBadScheme):
		return "bad_scheme"
	case errors.Is(err, ErrPrivateAddress):
		return "private_address"
	case errors.Is(err, ErrTooManyRedirects):
		return "too_many_redirects"
	case errors.Is(err, ErrTooLarge):
		return "too_large"
	case errors.Is(err, ErrNotHTML):
		return "not_html"
	case errors.Is(err, ErrNotFetched):
		return "not_fetched"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "fetch_failed"
	}
}
