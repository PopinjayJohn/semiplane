package assets_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/assets"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/httpapi/identity"
	"github.com/semiplane/semiplane/internal/httpapi/middleware"
	"github.com/semiplane/semiplane/internal/store"
	"github.com/semiplane/semiplane/internal/web/components"
)

// The harness drives the route through the same chain `router.go` builds: the
// campaign mux that `mountCampaignRoutes` fills, wrapped in `campaigns.Resolve`
// outside `campaigns.RequireRead`, mounted at `/c/` behind `middleware.RequestID`.
//
// The chain is assembled here rather than borrowed from the router because
// `router.go` is the integrator's file and this route is not mounted yet. The
// arrangement is the arrangement, so a change on either side shows up as a failing
// test rather than as a route that quietly lost its gate.
//
// The objects are real where the property under test is the real thing's: a real
// `content.Root` for the confinement and a real `os.File` for the range algebra.
// A fake appears only for the campaign rows, which nothing about a file on disk
// depends on.

// The campaigns these tests read, and the identifiers their rows carry.
const (
	testSlug    = "greyhaven"
	otherSlug   = "saltmarsh"
	testCampID  = int64(1)
	otherCampID = int64(2)
	gmUser      = int64(10)
	playerUser  = int64(11)
)

// gmRequestor is the campaign's GM.
func gmRequestor() domain.Requestor {
	return domain.Requestor{Authenticated: true, UserID: gmUser, Username: "mira"}
}

// playerRequestor is a member with the player role, which S-8 says is
// indistinguishable from anonymous as far as this route is concerned.
func playerRequestor() domain.Requestor {
	return domain.Requestor{Authenticated: true, UserID: playerUser, Username: "tobin"}
}

// anonymousRequestor is a signed-out reader of a public campaign: TierReadOnly.
func anonymousRequestor() domain.Requestor {
	return domain.Requestor{}
}

// campaignStore answers the two queries the access gate reads and refuses the
// three it does not need.
//
// Hand-written rather than pointed at a database so a schema change cannot alter
// what the access matrix resolves to, which is half of what the visibility tests
// below are about.
type campaignStore struct {
	campaigns map[string]domain.Campaign
	members   map[int64]domain.Role
}

func (s campaignStore) CampaignBySlug(_ context.Context, slug string) (domain.Campaign, error) {
	campaign, known := s.campaigns[slug]
	if !known {
		return domain.Campaign{}, store.ErrNotFound
	}

	return campaign, nil
}

func (s campaignStore) Membership(
	_ context.Context,
	campaignID, userID int64,
) (domain.Membership, error) {
	role, member := s.members[userID]
	if !member || campaignID != testCampID {
		return domain.Membership{}, store.ErrNotFound
	}

	return domain.Membership{CampaignID: campaignID, UserID: userID, Role: role}, nil
}

func (s campaignStore) CreateCampaign(
	_ context.Context,
	_ domain.Campaign,
) (domain.Campaign, error) {
	return domain.Campaign{}, errors.New("campaignStore: CreateCampaign is not used")
}

func (s campaignStore) CreateMembership(
	_ context.Context,
	_ domain.Membership,
) (domain.Membership, error) {
	return domain.Membership{}, errors.New("campaignStore: CreateMembership is not used")
}

func (s campaignStore) DeleteCampaign(_ context.Context, _ int64) error {
	return errors.New("campaignStore: DeleteCampaign is not used")
}

// harness is one assembled route and the vault behind it.
//
// Two campaigns, because "a cache entry is never shared across campaigns" (S-8.3)
// is a claim about two of them and a test with one cannot make it. Both roots are
// real and both are held by one registry, so a path that resolves in one campaign
// resolves there and nowhere else.
type harness struct {
	t          *testing.T
	registry   *content.Registry
	dirs       map[string]string
	visibility domain.Visibility
	known      map[string]bool
}

// newHarness builds a route over a public campaign with one asset in it.
func newHarness(t *testing.T) *harness {
	t.Helper()

	registry := content.NewRegistry(content.RefuseSymlinks)

	dirs := map[string]string{testSlug: t.TempDir(), otherSlug: t.TempDir()}
	for slug, dir := range dirs {
		if _, err := registry.Open(slug, dir); err != nil {
			t.Fatalf("open content root for %s: %v", slug, err)
		}
	}

	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Errorf("close content roots: %v", err)
		}
	})

	return &harness{
		t:          t,
		registry:   registry,
		dirs:       dirs,
		visibility: domain.VisibilityPublic,
		known:      map[string]bool{testSlug: true},
	}
}

// private makes the harness's campaign private, so a reader who is not a member is
// refused rather than served.
//
// The default is public and that is deliberate: a gate answering 404 for everything
// is indistinguishable from a route that is working, and a suite whose failures are
// invisible is worse than one that is slow.
func (h *harness) private() *harness {
	h.visibility = domain.VisibilityPrivate

	return h
}

// withSecondCampaign registers the other campaign, so a cross-campaign test has
// two slugs and two roots to ask about.
func (h *harness) withSecondCampaign() *harness {
	h.known[otherSlug] = true

	return h
}

// write puts a file into a campaign's content root, creating its directory.
//
// Written with `os` because the test is the vault's author here, not the route: the
// confinement under test is between a request and a file, and a test that could not
// put a file in the root could not test a route that reads one.
func (h *harness) write(name string, body []byte) {
	h.t.Helper()

	h.writeIn(testSlug, name, body)
}

// writeIn is `write` for a named campaign.
func (h *harness) writeIn(slug, name string, body []byte) {
	h.t.Helper()

	full := filepath.Join(h.dirs[slug], filepath.FromSlash(name))

	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		h.t.Fatalf("create directory for %s: %v", name, err)
	}

	if err := os.WriteFile(full, body, 0o600); err != nil {
		h.t.Fatalf("write %s in %s: %v", name, slug, err)
	}
}

// handler builds the route under test.
func (h *harness) handler() *assets.Handler {
	return &assets.Handler{
		Roots:       h.registry,
		Instance:    components.InstanceView{Name: "Greyhaven", Version: "0.3.0"},
		SignOutHref: "/logout",
	}
}

// serve builds the whole chain, in the order `router.go` assembles it.
//
// The outer pattern is `/c/{slug}/` and not `/c/`, and that is the one place this
// harness is not a copy of what is in `router.go`: `campaigns.Resolve` reads the
// campaign out of `r.PathValue("slug")`, and a `net/http` path value is set by the
// mux whose *pattern* matched. Mounted at `/c/`, no wildcard is named, Resolve
// resolves nothing, the tier stays TierNone and RequireRead answers 404 for every
// request under `/c/`.
func (h *harness) serve() http.Handler {
	return h.serveMountedAt("/c/{slug}/")
}

// serveMountedAt builds the same chain with a chosen outer pattern.
func (h *harness) serveMountedAt(outerPattern string) http.Handler {
	campaignMux := http.NewServeMux()
	assets.Mount(campaignMux, h.handler())

	// Resolve outside RequireRead outside the campaign mux, because the guard
	// beneath Resolve reads the tier it is deciding on.
	outer := http.NewServeMux()
	outer.Handle(outerPattern,
		campaigns.Resolve(h.store())(
			middleware.Chain(campaignMux, campaigns.RequireRead),
		),
	)

	return middleware.RequestID(outer)
}

// store builds the campaign rows this harness serves.
func (h *harness) store() campaignStore {
	rows := map[string]domain.Campaign{
		testSlug: {
			ID: testCampID, Slug: testSlug, Name: "Greyhaven", Visibility: h.visibility,
		},
	}

	if h.known[otherSlug] {
		rows[otherSlug] = domain.Campaign{
			ID:         otherCampID,
			Slug:       otherSlug,
			Name:       "Saltmarsh",
			Visibility: domain.VisibilityPublic,
		}
	}

	return campaignStore{
		campaigns: rows,
		members: map[int64]domain.Role{
			gmUser:     domain.RoleGM,
			playerUser: domain.RolePlayer,
		},
	}
}

// get issues one request as requestor and returns the recorded response.
func (h *harness) get(target string, requestor domain.Requestor) *httptest.ResponseRecorder {
	h.t.Helper()

	return h.request(http.MethodGet, target, requestor, nil)
}

// getWith issues one request carrying headers.
func (h *harness) getWith(
	target string,
	requestor domain.Requestor,
	header http.Header,
) *httptest.ResponseRecorder {
	h.t.Helper()

	return h.request(http.MethodGet, target, requestor, header)
}

// rangeGet issues one request carrying a Range header.
func (h *harness) rangeGet(target, spec string) *httptest.ResponseRecorder {
	h.t.Helper()

	return h.getWith(target, gmRequestor(), http.Header{"Range": {spec}})
}

// request issues one request against a freshly built chain.
//
// A fresh chain per request rather than one shared handler, because a shared one
// would hold the *first* handler's dependencies and a test that changes visibility
// between two requests would silently be testing the first.
func (h *harness) request(
	method, target string,
	requestor domain.Requestor,
	header http.Header,
) *httptest.ResponseRecorder {
	h.t.Helper()

	req := httptest.NewRequestWithContext(h.t.Context(), method, target, http.NoBody)
	req = req.WithContext(identity.WithRequestor(req.Context(), requestor))

	for name, values := range header {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}

	recorder := httptest.NewRecorder()
	h.serve().ServeHTTP(recorder, req)

	return recorder
}

// mapBody is the asset most of these tests serve: 1024 bytes whose every position
// is distinguishable, so an assertion about "the right slice" can name the slice
// rather than merely its length.
//
// A repeated pattern would not do. `bytes.Repeat([]byte("a"), 1024)` makes every
// 100-byte answer identical, so a handler that returned the wrong range with the
// right length would pass, and "the right slice" would be untested.
func mapBody() []byte {
	body := make([]byte, 1024)
	for i := range body {
		body[i] = byte('A' + i%26)
	}

	return body
}

// pngBytes is a byte sequence that is *not* a PNG.
//
// Used for the "the table decides the type, not the bytes" test: the file is named
// `.png` and contains something a sniffer would classify differently. A real PNG
// would let a `DetectContentType` fallback and a correct `Content-Type` produce
// the same answer, which is a test that cannot fail.
var pngBytes = []byte("<!DOCTYPE html><script>alert(1)</script>\n")

// TestAnAssetIsServedWholeToAReaderWhoPassedTheGate is the ordinary success: a
// 200 with the bytes, the type from the table, and the headers that say the
// response is revalidatable and private.
func TestAnAssetIsServedWholeToAReaderWhoPassedTheGate(t *testing.T) {
	t.Parallel()

	body := mapBody()
	fixed := newHarness(t)
	fixed.write("images/map.png", body)

	recorder := fixed.get("/c/greyhaven/assets/images/map.png", gmRequestor())

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body:\n%s", recorder.Code, recorder.Body)
	}

	if got := recorder.Body.Bytes(); !bytes.Equal(got, body) {
		t.Errorf("body = %d bytes, want the %d on disk", len(got), len(body))
	}

	for header, want := range map[string]string{
		"Content-Type":           "image/png",
		"Content-Length":         "1024",
		"Accept-Ranges":          "bytes",
		"X-Content-Type-Options": "nosniff",
		"Cache-Control":          "private, no-cache",
		"Vary":                   "Cookie",
	} {
		if got := recorder.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}

	if recorder.Header().Get("ETag") == "" {
		t.Error("no ETag on an asset response; §6.6 makes the validator the whole cache story")
	}

	if recorder.Header().Get("Last-Modified") == "" {
		t.Error("no Last-Modified; it is what ServeContent's If-Range comparison needs")
	}

	// A full 200 must not claim to be a slice. `Content-Range` on a 200 tells a
	// client it received a window, which is the one way a range implementation
	// lies without lying about a byte count.
	if got := recorder.Header().Get("Content-Range"); got != "" {
		t.Errorf("Content-Range = %q on a 200; a full representation is not a range", got)
	}
}

// TestAnAnonymousReaderOfAPublicCampaignGetsTheSameAsset covers the visibility
// rule S-8 states as "public grants wiki read only": the bytes are identical for
// every reader the gate admits, which is why the same validator is correct for all
// of them.
func TestAnAnonymousReaderOfAPublicCampaignGetsTheSameAsset(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("map.png", mapBody())

	anonymous := fixed.get("/c/greyhaven/assets/map.png", anonymousRequestor())
	player := fixed.get("/c/greyhaven/assets/map.png", playerRequestor())
	gm := fixed.get("/c/greyhaven/assets/map.png", gmRequestor())

	for name, recorder := range map[string]*httptest.ResponseRecorder{
		"anonymous": anonymous,
		"player":    player,
		"gm":        gm,
	} {
		if recorder.Code != http.StatusOK {
			t.Errorf("the %s reader got %d, want 200", name, recorder.Code)
		}
	}

	if gm.Header().Get("ETag") != anonymous.Header().Get("ETag") {
		t.Errorf(
			"a GM and an anonymous reader of a public campaign got different validators: %q and %q. "+
				"The bytes are identical, so they are one representation",
			gm.Header().Get("ETag"),
			anonymous.Header().Get("ETag"),
		)
	}
}

// TestAPrivateCampaignsAssetIsRefusedToAnAnonymousReader is S-8's asset row: the
// gate refuses, and it refuses with a 404 rather than a 403 so the campaign's
// existence is not observable.
func TestAPrivateCampaignsAssetIsRefusedToAnAnonymousReader(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t).private()
	fixed.write("map.png", mapBody())

	anonymous := fixed.get("/c/greyhaven/assets/map.png", anonymousRequestor())

	if anonymous.Code != http.StatusNotFound {
		t.Fatalf("an anonymous reader of a private campaign's asset got %d, want 404",
			anonymous.Code)
	}

	if strings.Contains(anonymous.Body.String(), "AAAA") {
		t.Error("the refusal carries the asset's bytes")
	}

	// The member is still served, which is what makes the refusal a visibility
	// decision rather than a broken route.
	if member := fixed.get(
		"/c/greyhaven/assets/map.png",
		gmRequestor(),
	); member.Code != http.StatusOK {
		t.Errorf("the GM got %d, want 200", member.Code)
	}
}

// TestAPrivateCampaignAndACampaignThatDoesNotExistAreByteIdentical is the
// existence-oracle rule (S-14.3, S-8): a private campaign and one that was never
// registered must answer the same request the same way, in every observable byte —
// status, headers, body.
//
// Byte-identity rather than "both 404". Two 404s with different bodies is an
// oracle with a very short body, and it is the shape of mistake this rule exists to
// catch: the gate knows which campaign it refused and the not-found handler does
// not, so any text that differs is a fact about existence.
func TestAPrivateCampaignAndACampaignThatDoesNotExistAreByteIdentical(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t).private()
	fixed.write("map.png", mapBody())

	privateCampaign := fixed.get("/c/greyhaven/assets/map.png", anonymousRequestor())
	noSuchCampaign := fixed.get("/c/no-such-campaign/assets/map.png", anonymousRequestor())

	if privateCampaign.Code != noSuchCampaign.Code {
		t.Errorf("statuses differ: %d for a private campaign and %d for one that does not exist",
			privateCampaign.Code, noSuchCampaign.Code)
	}

	if privateCampaign.Body.String() != noSuchCampaign.Body.String() {
		t.Errorf("the bodies differ.\nprivate: %q\nabsent:  %q",
			privateCampaign.Body.String(), noSuchCampaign.Body.String())
	}

	// `X-Request-Id` is deliberately not in the list: the middleware mints a fresh
	// one per request, so comparing it would assert that two requests are the same
	// request. Every other header is a statement about the answer.
	for _, header := range []string{"Content-Type", "Cache-Control", "Vary"} {
		if privateCampaign.Header().Get(header) != noSuchCampaign.Header().Get(header) {
			t.Errorf("%s differs: %q for a private campaign and %q for one that does not exist",
				header,
				privateCampaign.Header().Get(header),
				noSuchCampaign.Header().Get(header))
		}
	}
}

// TestAnAssetInOneCampaignIsNotReadableThroughAnothersURL is S-8.3's other half:
// the same relative path must not resolve in a campaign that does not hold it. The
// two vaults get deliberately different bytes so the assertion can say *whose* file
// answered, rather than merely that something did.
func TestAnAssetInOneCampaignIsNotReadableThroughAnothersURL(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t).withSecondCampaign()
	fixed.write("images/map.png", []byte("greyhaven's map"))
	fixed.writeIn(otherSlug, "images/map.png", []byte("saltmarsh's map"))

	mine := fixed.get("/c/greyhaven/assets/images/map.png", gmRequestor())
	theirs := fixed.get("/c/saltmarsh/assets/images/map.png", gmRequestor())

	if mine.Body.String() != "greyhaven's map" {
		t.Errorf("campaign A served %q, want its own file", mine.Body.String())
	}

	if theirs.Body.String() != "saltmarsh's map" {
		t.Errorf("campaign B served %q, want its own file", theirs.Body.String())
	}
}

// TestAnAssetHeldOnlyByOneCampaignIsNotFoundThroughTheOther is the leak the test
// above cannot see: when campaign B has *no* such file, B's URL must not fall back
// to A's. A lookup that searched "the campaign the reader can see" rather than
// "the campaign in the URL" would answer 200 here, and the bytes would be another
// campaign's map.
func TestAnAssetHeldOnlyByOneCampaignIsNotFoundThroughTheOther(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t).withSecondCampaign()
	fixed.write("images/greymap.png", []byte("greyhaven's map"))

	leaked := fixed.get("/c/saltmarsh/assets/images/greymap.png", gmRequestor())

	if leaked.Code != http.StatusNotFound {
		t.Errorf("campaign B's URL for campaign A's file answered %d, want 404", leaked.Code)
	}

	if strings.Contains(leaked.Body.String(), "greyhaven's map") {
		t.Error("campaign B's URL returned campaign A's bytes")
	}
}

// TestTwoCampaignsHoldingTheSameFileDoNotShareAValidator is S-8.3 restated as
// something assertable about the bytes of a header.
//
// Two vaults with byte-identical files in the same relative path. The bodies are
// equal, so a validator derived from the content alone would be equal too, and a
// cache that ignored the URL — a CDN configured with the path but not the campaign,
// or a future server-side cache keyed by path — would serve one campaign's asset
// for another's. Salting the pre-image with the campaign id is what makes the two
// provably distinct.
func TestTwoCampaignsHoldingTheSameFileDoNotShareAValidator(t *testing.T) {
	t.Parallel()

	body := mapBody()

	fixed := newHarness(t).withSecondCampaign()
	fixed.write("images/map.png", body)
	fixed.writeIn(otherSlug, "images/map.png", body)

	// Both written with the same bytes; the modification times are set to the same
	// instant so the only difference between the two validators is the campaign.
	fixed.stamp(testSlug, "images/map.png", testStamp)
	fixed.stamp(otherSlug, "images/map.png", testStamp)

	mine := fixed.get("/c/greyhaven/assets/images/map.png", gmRequestor())
	theirs := fixed.get("/c/saltmarsh/assets/images/map.png", gmRequestor())

	if mine.Body.String() != theirs.Body.String() {
		t.Fatal("the fixtures are not byte-identical, so this test would prove nothing")
	}

	if mine.Header().Get("ETag") == theirs.Header().Get("ETag") {
		t.Errorf("two campaigns holding identical bytes share the validator %q; S-8.3 says an "+
			"asset is never shared across campaigns", mine.Header().Get("ETag"))
	}
}

// testStamp is a fixed modification time, so a validator comparison is about the
// campaign and not about when a fixture happened to be written.
var testStamp = time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC)

// stamp sets a file's modification time and access time.
func (h *harness) stamp(slug, name string, when time.Time) {
	h.t.Helper()

	full := filepath.Join(h.dirs[slug], filepath.FromSlash(name))
	if err := os.Chtimes(full, when, when); err != nil {
		h.t.Fatalf("stamp %s in %s: %v", name, slug, err)
	}
}

// TestTheValidatorChangesWhenTheSizeChanges is one component of the validator's
// pre-image, on its own.
//
// The validator is (campaign, path, size, mtime) and has no content hash, because
// hashing a battle map per request would cost more than the whole route. That is a
// choice with a boundary, and these three tests are the boundary: a change to any
// one of the three inputs must produce a new validator, and the cases below vary
// exactly one of them at a time.
//
// The boundary itself: a rewrite that changes *neither* the size nor the
// modification time is invisible to this validator. It would take a content hash to
// see it, and the cost of that is the whole reason the validator is weak — see
// assetValidator. Stated here rather than left for a reader to infer, because
// "the validator is a function of the bytes" is the claim that would otherwise be
// made about it.
func TestTheValidatorChangesWhenTheSizeChanges(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("map.png", []byte(strings.Repeat("A", 512)))

	// The clock is pinned before the first read as well as before the second, so
	// that the modification time is provably *not* what changed. Pinning only the
	// second would let the modification time carry the assertion on its own, and
	// the size would go untested.
	fixed.stamp(testSlug, "map.png", testStamp)

	before := fixed.get("/c/greyhaven/assets/map.png", gmRequestor()).Header().Get("ETag")

	fixed.write("map.png", []byte(strings.Repeat("A", 900)))
	fixed.stamp(testSlug, "map.png", testStamp)

	after := fixed.get("/c/greyhaven/assets/map.png", gmRequestor()).Header().Get("ETag")

	if before == after {
		t.Error("a different length with the same modification time kept the validator; the size " +
			"is one of the three inputs and it is not being read")
	}
}

// TestTheValidatorChangesWhenTheModificationTimeChanges is the second component,
// and it is the one that catches the same-length rewrite a size cannot see.
func TestTheValidatorChangesWhenTheModificationTimeChanges(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("map.png", []byte(strings.Repeat("A", 512)))
	fixed.stamp(testSlug, "map.png", testStamp)

	before := fixed.get("/c/greyhaven/assets/map.png", gmRequestor()).Header().Get("ETag")

	// Same length, new bytes, same clock: a sync client rewriting a file in place is
	// exactly this, and it is what a size-only validator cannot see.
	fixed.write("map.png", []byte(strings.Repeat("B", 512)))
	fixed.stamp(testSlug, "map.png", testStamp.Add(time.Second))

	recorder := fixed.get("/c/greyhaven/assets/map.png", gmRequestor())

	if recorder.Header().Get("ETag") == before {
		t.Error("a same-length rewrite kept the validator")
	}

	if recorder.Body.String() != strings.Repeat("B", 512) {
		t.Error("the response is not the rewritten asset")
	}
}

// TestTheValidatorDistinguishesTwoFilesTheSameSizeWouldConflate is why the path is
// in the pre-image here, when `content.CacheKey.ETag` deliberately leaves it out.
//
// For a page, two pages sharing a content hash have the same body, so sharing a
// validator is correct and a rename need not invalidate anything. For an asset the
// body is *not* a function of (size, mtime): two different battle maps can be the
// same size and arrive from a sync in the same second. If they shared a validator,
// a client holding `a/map.png` would send `If-None-Match` for `b/map.png`, be told
// 304, and use the first map's bytes for the second — a wrong-bytes answer that
// looks exactly like a cache hit.
//
// The fixtures make that possible on purpose: same length, same clock, different
// contents, and the validator has to tell them apart anyway.
func TestTheValidatorDistinguishesTwoFilesTheSameSizeWouldConflate(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("a/map.png", []byte(strings.Repeat("A", 512)))
	fixed.write("b/map.png", []byte(strings.Repeat("B", 512)))
	fixed.stamp(testSlug, "a/map.png", testStamp)
	fixed.stamp(testSlug, "b/map.png", testStamp)

	first := fixed.get("/c/greyhaven/assets/a/map.png", gmRequestor())
	second := fixed.get("/c/greyhaven/assets/b/map.png", gmRequestor())

	if first.Header().Get("ETag") == second.Header().Get("ETag") {
		t.Error(
			"two same-sized maps written at the same instant share a validator; a conditional " +
				"request for the second would be answered 304 with the first map's bytes",
		)
	}

	// And the 304 is the case that turns a shared validator into wrong bytes rather
	// than into a missed optimisation.
	conditional := fixed.getWith("/c/greyhaven/assets/b/map.png", gmRequestor(), http.Header{
		"If-None-Match": {first.Header().Get("ETag")},
	})

	if conditional.Code != http.StatusOK {
		t.Errorf("a revalidation with the other map's validator answered %d, want 200",
			conditional.Code)
	}

	if conditional.Body.String() != strings.Repeat("B", 512) {
		t.Error("the response is not the second map")
	}
}

// --- Range -------------------------------------------------------------------

// TestEveryRangeFormIsAnsweredCorrectly is the reason this route exists at all
// (architecture §16.2: maps are tens of megabytes and a VTT needs to seek).
//
// Each case asserts the status, `Content-Range`, `Content-Length` and the bytes,
// because a range implementation that gets the status right and the slice wrong is
// the failure that corrupts a map silently. The bytes are checked against
// `mapBody()[start:end]`, which is distinguishable at every offset.
func TestEveryRangeFormIsAnsweredCorrectly(t *testing.T) {
	t.Parallel()

	body := mapBody()

	for _, testCase := range []struct {
		name         string
		spec         string
		wantStatus   int
		wantRange    string
		wantLength   string
		wantType     string
		wantBody     []byte
		wantContains []string
		absent       []string
	}{
		{
			name: "a single byte range", spec: "bytes=0-99",
			wantStatus: http.StatusPartialContent,
			wantRange:  "bytes 0-99/1024", wantLength: "100",
			wantBody: body[0:100],
		},
		{
			name: "a single byte at the end", spec: "bytes=1023-1023",
			wantStatus: http.StatusPartialContent,
			wantRange:  "bytes 1023-1023/1024", wantLength: "1",
			wantBody: body[1023:1024],
		},
		{
			name: "an open-ended range", spec: "bytes=100-",
			wantStatus: http.StatusPartialContent,
			wantRange:  "bytes 100-1023/1024", wantLength: "924",
			wantBody: body[100:],
		},
		{
			name: "a suffix range", spec: "bytes=-500",
			wantStatus: http.StatusPartialContent,
			wantRange:  "bytes 524-1023/1024", wantLength: "500",
			wantBody: body[524:],
		},
		{
			// A suffix larger than the file is the whole file, not an error: RFC 9110
			// §14.1.1 says the suffix is the *last* N bytes, and N beyond the start
			// clamps rather than failing. A 416 here would break every client that asks
			// for "the last 40 MB" of a 12 MB map.
			name: "a suffix longer than the file", spec: "bytes=-5000",
			wantStatus: http.StatusPartialContent,
			wantRange:  "bytes 0-1023/1024", wantLength: "1024",
			wantBody: body,
		},
		{
			// Multi-range is answered as multipart/byteranges, which is why there is no
			// `Content-Range` here: a multi-range response has several, inside the
			// multipart body. Asserting its absence is the point — a single-range
			// implementation that answered this would put one `Content-Range` on a body
			// carrying two slices, and the client's frame decode would be wrong.
			name: "a multi-range request", spec: "bytes=0-9,20-29",
			wantStatus:   http.StatusPartialContent,
			wantLength:   "336",
			wantType:     "multipart/byteranges; boundary=",
			wantContains: []string{"bytes 0-9/1024", "bytes 20-29/1024", "\r\n\r\n"},
			absent:       []string{"Content-Range"},
		},
		{
			// A 416 must carry `Content-Range: bytes */<size>` — RFC 9110 §15.5.17
			// makes the size the one piece of information a client needs to ask again
			// correctly, and a bare 416 tells it nothing. `ServeContent` writes it.
			name: "an unsatisfiable range", spec: "bytes=5000-6000",
			wantStatus: http.StatusRequestedRangeNotSatisfiable,
			wantRange:  "bytes */1024",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			fixed := newHarness(t)
			fixed.write("map.png", body)

			recorder := fixed.rangeGet("/c/greyhaven/assets/map.png", testCase.spec)

			if recorder.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, testCase.wantStatus)
			}

			if recorder.Header().Get("Accept-Ranges") != "bytes" {
				t.Errorf("Accept-Ranges = %q, want %q: a client that just got a 416 needs to know "+
					"ranges are supported at all",
					recorder.Header().Get("Accept-Ranges"), "bytes")
			}

			if testCase.wantRange != "" {
				if got := recorder.Header().Get("Content-Range"); got != testCase.wantRange {
					t.Errorf("Content-Range = %q, want %q", got, testCase.wantRange)
				}
			}

			if testCase.wantLength != "" {
				if got := recorder.Header().Get("Content-Length"); got != testCase.wantLength {
					t.Errorf("Content-Length = %q, want %q", got, testCase.wantLength)
				}
			}

			if testCase.wantType != "" {
				if got := recorder.Header().
					Get("Content-Type"); !strings.HasPrefix(
					got,
					testCase.wantType,
				) {
					t.Errorf("Content-Type = %q, want the prefix %q", got, testCase.wantType)
				}
			}

			for _, header := range testCase.absent {
				if got := recorder.Header().Get(header); got != "" {
					t.Errorf("%s = %q, want it absent", header, got)
				}
			}

			for _, fragment := range testCase.wantContains {
				if !strings.Contains(recorder.Body.String(), fragment) {
					t.Errorf("the body does not contain %q:\n%s", fragment, recorder.Body.String())
				}
			}

			if testCase.wantBody != nil && recorder.Body.String() != string(testCase.wantBody) {
				t.Errorf("body = %d bytes, want %d; a range that gets the status right and the "+
					"slice wrong corrupts a map silently", recorder.Body.Len(), len(testCase.wantBody))
			}
		})
	}
}

// TestAMalformedRangeIsRefusedRatherThanGuessed documents a deliberate deviation.
//
// RFC 9110 §14.2 says a server that cannot parse a Range header should ignore it
// and serve the whole representation. `http.ServeContent` answers 416 instead.
// The alternative — a second range parser in this package to be conformant — is
// worse than the deviation: 416 is the safe direction (it never serves bytes a
// client did not ask for, and it never serves bytes at all), and the cost of the
// deviation is a client that sends a malformed header being told so.
//
// Asserted so that a future change to the range path has to decide about it
// rather than inherit it.
func TestAMalformedRangeIsRefusedRatherThanGuessed(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("map.png", mapBody())

	recorder := fixed.rangeGet("/c/greyhaven/assets/map.png", "bytes=not-a-range")

	if recorder.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("status = %d, want 416", recorder.Code)
	}

	// `ServeContent` writes a short plain-text explanation on a 416, which is
	// allowed. What must not be there is a byte of the asset: a refused range
	// serves none of the representation, and a body that carried one would be
	// indistinguishable from a 200 to anything that only reads bodies.
	if strings.Contains(recorder.Body.String(), "AAAAAAAAAA") {
		t.Errorf("the 416 carried a slice of the asset:\n%s", recorder.Body.String())
	}
}

// TestIfRangeChoosesBetweenASliceAndTheWholeRepresentation is RFC 9110 §13.1.5,
// and the reason the test fixes a file's clock.
//
// A hit means the client's cached copy is current, so the range is answered. A miss
// means the file changed since, so the *whole* representation is sent and the client
// rebuilds from it. Getting this backwards is the classic resumed-download
// corruption: a client stitches a new head onto an old tail.
//
// The hit is expressed with a date rather than with the `ETag`, and that is not a
// dodge: §13.1.5 forbids a client from putting a *weak* validator in `If-Range`, and
// this route's validator is weak (see assetValidator). The date is the form a
// conformant client sends.
func TestIfRangeChoosesBetweenASliceAndTheWholeRepresentation(t *testing.T) {
	t.Parallel()

	body := mapBody()

	fixed := newHarness(t)
	fixed.write("map.png", body)
	fixed.stamp(testSlug, "map.png", testStamp)

	hit := fixed.getWith("/c/greyhaven/assets/map.png", gmRequestor(), http.Header{
		"Range":    {"bytes=0-99"},
		"If-Range": {testStamp.Format(http.TimeFormat)},
	})

	if hit.Code != http.StatusPartialContent {
		t.Fatalf("a matching If-Range answered %d, want 206; body:\n%s", hit.Code, hit.Body)
	}

	if got := hit.Header().Get("Content-Range"); got != "bytes 0-99/1024" {
		t.Errorf("Content-Range = %q, want %q", got, "bytes 0-99/1024")
	}

	if hit.Body.String() != string(body[0:100]) {
		t.Error("the slice did not match")
	}

	stale := fixed.getWith("/c/greyhaven/assets/map.png", gmRequestor(), http.Header{
		"Range":    {"bytes=0-99"},
		"If-Range": {testStamp.Add(-time.Hour).Format(http.TimeFormat)},
	})

	if stale.Code != http.StatusOK {
		t.Errorf("a stale If-Range answered %d, want 200 with the whole representation", stale.Code)
	}

	if stale.Body.String() != string(body) {
		t.Errorf("a stale If-Range served %d bytes, want the whole %d", stale.Body.Len(), len(body))
	}

	if got := stale.Header().Get("Content-Range"); got != "" {
		t.Errorf("the full response claims a range: %q", got)
	}
}

// TestAWeakValidatorInIfRangeGetsTheWholeRepresentation documents the other half
// of the validator's strength, so the §13.1.5 behaviour is a recorded decision
// rather than a surprise.
//
// RFC 9110 §13.1.5 says a client MUST NOT put a weak validator in `If-Range`, and
// the standard library enforces it: a weak `ETag` never strong-matches, so the
// range is dropped and the whole representation is sent. That is the safe direction
// and it is asserted here because the alternative reading — "the validator is
// ignored" — would be the wrong one.
func TestAWeakValidatorInIfRangeGetsTheWholeRepresentation(t *testing.T) {
	t.Parallel()

	body := mapBody()

	fixed := newHarness(t)
	fixed.write("map.png", body)
	fixed.stamp(testSlug, "map.png", testStamp)

	validator := fixed.get("/c/greyhaven/assets/map.png", gmRequestor()).Header().Get("ETag")
	if !strings.HasPrefix(validator, "W/") {
		t.Fatalf(
			"the asset validator %q is not weak, so this test is about something else",
			validator,
		)
	}

	recorder := fixed.getWith("/c/greyhaven/assets/map.png", gmRequestor(), http.Header{
		"Range":    {"bytes=0-99"},
		"If-Range": {validator},
	})

	if recorder.Code != http.StatusOK {
		t.Errorf(
			"a weak If-Range answered %d, want 200; the whole representation is the safe answer",
			recorder.Code,
		)
	}

	if recorder.Body.String() != string(body) {
		t.Error("the response was not the whole representation")
	}
}

// TestAConditionalRequestForAnUnchangedAssetIsNotModified is the revalidation the
// `Cache-Control` is paying for: a reader who navigates back to a map they have
// already downloaded pays a 304 and no bytes.
//
// `private, no-cache` is what makes the browser keep the body and revalidate, so
// without a 304 the whole point of the header is lost and every revisit is 40 MB.
func TestAConditionalRequestForAnUnchangedAssetIsNotModified(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("map.png", mapBody())

	first := fixed.get("/c/greyhaven/assets/map.png", gmRequestor())
	validator := first.Header().Get("ETag")

	second := fixed.getWith("/c/greyhaven/assets/map.png", gmRequestor(), http.Header{
		"If-None-Match": {validator},
	})

	if second.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", second.Code)
	}

	if second.Body.Len() != 0 {
		t.Errorf("a 304 carried %d bytes of body", second.Body.Len())
	}

	if got := second.Header().Get("ETag"); got != validator {
		t.Errorf("ETag = %q, want %q: a 304 has to tell the client which representation is current",
			got, validator)
	}

	if got := second.Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Errorf("Cache-Control = %q on a 304; a revalidation that drops the directives teaches "+
			"the browser to store nothing next time", got)
	}

	if got := second.Header().Get("Vary"); got != "Cookie" {
		t.Errorf("Vary = %q on a 304, want %q", got, "Cookie")
	}

	if got := second.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges = %q on a 304, want %q", got, "bytes")
	}
}

// TestAStaleValidatorServesTheWholeAsset is the other direction: a validator from
// before an edit must not revalidate the new bytes.
func TestAStaleValidatorServesTheWholeAsset(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("map.png", []byte(strings.Repeat("A", 512)))

	stale := fixed.get("/c/greyhaven/assets/map.png", gmRequestor()).Header().Get("ETag")

	fixed.write("map.png", []byte(strings.Repeat("B", 512)))
	fixed.stamp(testSlug, "map.png", testStamp.Add(time.Second))

	recorder := fixed.getWith("/c/greyhaven/assets/map.png", gmRequestor(), http.Header{
		"If-None-Match": {stale},
	})

	if recorder.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; a stale validator must not revalidate", recorder.Code)
	}

	if recorder.Body.String() != strings.Repeat("B", 512) {
		t.Error("the response is not the rewritten asset")
	}
}

// --- Content type ------------------------------------------------------------

// TestContentTypesComeFromAClosedTable is the table itself, one row per extension.
//
// A dozen would be a sample; the table is the security boundary, so the test is the
// table. Each row asserts the exact `Content-Type`, which is what makes the
// assertion about *this* table rather than about a type the browser would have
// guessed anyway.
func TestContentTypesComeFromAClosedTable(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct{ name, want string }{
		{".png", "image/png"},
		{".jpg", "image/jpeg"},
		{".jpeg", "image/jpeg"},
		{".gif", "image/gif"},
		{".webp", "image/webp"},
		{".avif", "image/avif"},
		{".svg", "image/svg+xml"},
		{".pdf", "application/pdf"},
		{".txt", "text/plain; charset=utf-8"},
		{".csv", "text/csv; charset=utf-8"},
		{".json", "application/json"},
		{".css", "text/css; charset=utf-8"},
		// Deliberately the row that a host's own mime table would get wrong: Go's
		// builtin table says `text/xml; charset=utf-8` for this extension, and
		// `/etc/mime.types` can say anything at all. See media.go for why the table
		// here is the only answer a deployment can rely on.
		{".xml", "application/xml"},
		{".zip", "application/zip"},
		{".mp4", "video/mp4"},
		{".webm", "video/webm"},
		{".mp3", "audio/mpeg"},
		{".woff2", "font/woff2"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			fixed := newHarness(t)
			fixed.write("file"+testCase.name, []byte("contents"))

			recorder := fixed.get("/c/greyhaven/assets/file"+testCase.name, gmRequestor())

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", recorder.Code)
			}

			if got := recorder.Header().Get("Content-Type"); got != testCase.want {
				t.Errorf("Content-Type = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestTheExtensionIsDecidedCaseInsensitively is a filesystem fact, not a nicety.
//
// A vault synced from a case-insensitive filesystem can hold `Map.PNG`, and
// `os.Root` resolves it. Serving it is the only answer; a 404 would make the
// behaviour depend on which operating system the operator picked, which is the
// thing S-3.5 is about.
func TestTheExtensionIsDecidedCaseInsensitively(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Map.PNG", []byte("contents"))

	recorder := fixed.get("/c/greyhaven/assets/Map.PNG", gmRequestor())

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	if got := recorder.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want %q", got, "image/png")
	}
}

// TestTheTypeComesFromTheExtensionAndNotFromTheBytes is the property that makes
// the table a boundary: a `.png` whose bytes are an HTML document is still a PNG.
//
// It also pins the sniffing path shut. `http.ServeContent` falls back to
// `http.DetectContentType` on the first 512 bytes whenever the `Content-Type` is
// unset, and that is `DetectContentType` reading attacker-supplied content — S-4.7
// makes every byte in a content root reachable by an Obsidian sync client, a
// community plugin, or any device that syncs that vault.
func TestTheTypeComesFromTheExtensionAndNotFromTheBytes(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("disguised.png", pngBytes)

	recorder := fixed.get("/c/greyhaven/assets/disguised.png", gmRequestor())

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	if got := recorder.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want %q; the extension decides and the bytes never do",
			got, "image/png")
	}

	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want %q: without it the browser may still re-sniff",
			got, "nosniff")
	}

	if strings.Contains(recorder.Header().Get("Content-Type"), "html") {
		t.Error("an HTML payload reached a reader as HTML")
	}
}

// TestAFileWithNoExtensionIsNeverServedByItsContent is the sniff path's real
// test, and it is the one that fails if the closed table is not consulted.
//
// `http.ServeContent` sniffs when it has no `Content-Type`, and it consults
// `mime.TypeByExtension` before the sniffer. A file with no extension reaches the
// sniffer directly: an HTML payload named `download` would be served as
// `text/html` from semiplane's origin, which is stored XSS with a session cookie in
// scope. The closed table has no entry for "no extension", so the route never
// reaches `ServeContent` at all — which is what the missing `Accept-Ranges` below
// shows, since that header is written on the byte-serving path only.
func TestAFileWithNoExtensionIsNeverServedByItsContent(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("download", []byte("<!DOCTYPE html><script>alert(document.cookie)</script>\n"))

	recorder := fixed.get("/c/greyhaven/assets/download", gmRequestor())

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; an extensionless file reached the bytes", recorder.Code)
	}

	if strings.Contains(recorder.Body.String(), "alert") {
		t.Error("the file's bytes are in the response")
	}

	// The failure state is `text/html` — it is the shell, and it has to be. What it
	// must not be is the *file*, and `Accept-Ranges` is the discriminator: the byte
	// path writes it and the failure path does not, so its absence says the request
	// never reached `ServeContent`. A mutation that let an extensionless file
	// through would answer 200 here with the payload as `text/html`.
	if got := recorder.Header().Get("Accept-Ranges"); got != "" {
		t.Errorf("Accept-Ranges = %q on the failure state; the byte-serving path was reached", got)
	}

	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want %q: a failure carries no validator", got, "no-store")
	}
}

// TestNoAssetIsServedAsADocumentOrAsAScript is the stored-XSS rule as a rule rather
// than as a list of three extensions.
//
// The claim is about the *response carrying the file's bytes*, not about the
// failure states — a failure state is the shell, and it is legitimately
// `text/html`. So the assertion is conditional on the status:
//
//   - a 200 must not carry a media type a browser executes or renders as a
//     document, unless it is one of the two the table serves *with* the pair of
//     defences that makes it inert (`Content-Disposition: attachment` and
//     `Content-Security-Policy: sandbox`);
//   - anything else must not contain a byte of the file.
//
// The extension list is deliberately wider than the table: the html family, the
// script family, the SVG and XML the table *does* serve, and the inert types it
// serves plainly. A rule asserted only over the entries it forbids is a rule that
// never sees the interesting case.
func TestNoAssetIsServedAsADocumentOrAsAScript(t *testing.T) {
	t.Parallel()

	// documentTypes are the media types a browser treats as a document or as code.
	// `image/svg+xml` and `application/xml` are here on purpose: they are in the
	// table, and the table's answer for them is the pair of defences, not the type.
	documentTypes := []string{
		"text/html",
		"application/xhtml+xml",
		"text/javascript",
		"application/javascript",
		"application/ecmascript",
		"application/wasm",
		"image/svg+xml",
		"application/xml",
	}

	for _, name := range []string{
		"page.html", "page.htm", "page.xhtml",
		"payload.js", "payload.mjs", "payload.cjs", "payload.wasm",
		"map.svg", "data.xml",
		"map.png", "handout.pdf", "notes.txt", "data.json", "style.css", "archive.zip",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			const payload = "<!DOCTYPE html><script>alert(1)</script>"

			fixed := newHarness(t)
			fixed.write(name, []byte(payload))

			recorder := fixed.get("/c/greyhaven/assets/"+name, gmRequestor())

			if recorder.Code != http.StatusOK {
				// Refused. The bytes must not be in the response, and the response is
				// the shell's failure state rather than the file.
				if strings.Contains(recorder.Body.String(), payload) {
					t.Error("the refusal carries the file's bytes")
				}

				return
			}

			declared := recorder.Header().Get("Content-Type")

			for _, document := range documentTypes {
				if !strings.HasPrefix(declared, document) {
					continue
				}

				if got := recorder.Header().Get("Content-Disposition"); got != "attachment" {
					t.Errorf("Content-Type = %q with Content-Disposition = %q; a %s served "+
						"inline is stored XSS", declared, got, document)
				}

				if got := recorder.Header().Get("Content-Security-Policy"); got != "sandbox" {
					t.Errorf("Content-Type = %q with Content-Security-Policy = %q; the %s needs "+
						"the sandbox backstop as well as the disposition",
						declared, got, document)
				}
			}
		})
	}
}

// TestAnHTMLFileIsRefused is the other half of the rule above: not "served with a
// safe type" but refused.
//
// A 403 rather than a 404, because the file is there and a GM who dropped one into
// the vault is owed the reason rather than a claim that it does not exist. And a
// refusal here is *stronger* than downgrading the type: with the table consulted
// before `ServeContent`, there is no response for a browser to misinterpret at all.
func TestAnHTMLFileIsRefused(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"page.html", "page.htm", "page.xhtml"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixed := newHarness(t)
			fixed.write(name, []byte("<!DOCTYPE html><script>alert(1)</script>\n"))

			recorder := fixed.get("/c/greyhaven/assets/"+name, gmRequestor())

			if recorder.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403", recorder.Code)
			}

			if strings.Contains(recorder.Body.String(), "alert(1)") {
				t.Error("the refusal carries the file's bytes")
			}
		})
	}
}

// TestAnSVGIsForcedToADownload is the inline-rendering defence for the one active
// format a campaign genuinely wants.
//
// An SVG is a document: it can carry a `<script>`, an `onload`, and a
// `<foreignObject>`. Served inline from semiplane's origin it executes with the
// origin's cookies. Three headers stand between it and that, and each covers a
// different failure:
//
//   - `Content-Disposition: attachment` — a *navigation* downloads rather than
//     renders. This is the primary defence and it costs nothing: a battle map
//     dropped in an `<img>` renders identically, because the standard applies
//     `Content-Disposition` to navigations and not to subresource loads.
//   - `Content-Security-Policy: sandbox` — for a browser that renders it anyway.
//     `sandbox` with no tokens is an opaque origin with no script, so a document
//     that does get created cannot reach back into semiplane.
//   - `nosniff` — so the declared type is the only type.
//
// The `image/svg+xml` type is kept rather than downgraded to octet-stream precisely
// because the first two headers make inline rendering unreachable, and the type is
// what lets the file draw as an image at all.
func TestAnSVGIsForcedToADownload(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("map.svg", []byte(
		`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`,
	))

	recorder := fixed.get("/c/greyhaven/assets/map.svg", gmRequestor())

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; an SVG is a legitimate asset", recorder.Code)
	}

	if got := recorder.Header().Get("Content-Type"); got != "image/svg+xml" {
		t.Errorf("Content-Type = %q, want %q", got, "image/svg+xml")
	}

	if got := recorder.Header().Get("Content-Disposition"); got != "attachment" {
		t.Errorf("Content-Disposition = %q, want %q; an SVG rendered on navigation is stored XSS",
			got, "attachment")
	}

	if got := recorder.Header().Get("Content-Security-Policy"); got != "sandbox" {
		t.Errorf(
			"Content-Security-Policy = %q, want %q: the backstop for a browser that renders it anyway",
			got,
			"sandbox",
		)
	}

	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want %q", got, "nosniff")
	}
}

// TestAnXMLElementIsForcedToADownloadToo is the same rule applied to the other
// type in the table that renders as a document.
//
// `application/xml` renders through whatever stylesheet the document names, and
// XSLT is a scripting language, so the reasoning is identical. Named as its own
// test because "I fixed SVG" is the natural way to forget XML.
func TestAnXMLElementIsForcedToADownloadToo(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("data.xml", []byte(`<?xml version="1.0"?><data/>`))

	recorder := fixed.get("/c/greyhaven/assets/data.xml", gmRequestor())

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	// The type as well as the two defences, because this is the extension where
	// deferring to the platform's mime table produces a *different* answer: Go's
	// builtin table says `text/xml; charset=utf-8`, and `/etc/mime.types` can say
	// something else again. The closed table is what makes the answer the same on
	// every host.
	if got := recorder.Header().Get("Content-Type"); got != "application/xml" {
		t.Errorf("Content-Type = %q, want %q", got, "application/xml")
	}

	for header, want := range map[string]string{
		"Content-Disposition":     "attachment",
		"Content-Security-Policy": "sandbox",
	} {
		if got := recorder.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

// TestAnInertImageCarriesNoSandbox is the other direction, and it is the reason the
// sandbox header is set per media type rather than on the route.
//
// A `sandbox` policy on an image response is at best inert and at worst breaks the
// image: `default-src 'none'` alongside it would block the very resource the reader
// asked for, because `img-src` falls back to `default-src`. A battle map that does
// not draw is the bug this route exists to avoid.
func TestAnInertImageCarriesNoSandbox(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("map.png", mapBody())

	recorder := fixed.get("/c/greyhaven/assets/map.png", gmRequestor())

	if got := recorder.Header().Get("Content-Security-Policy"); got != "" {
		t.Errorf(
			"Content-Security-Policy = %q on a PNG; the policy belongs to documents, not to images",
			got,
		)
	}

	if got := recorder.Header().Get("Content-Disposition"); got != "" {
		t.Errorf("Content-Disposition = %q on a PNG; a battle map in an <img> must not be turned "+
			"into a download by a header the browser ignores there", got)
	}
}

// TestAMarkdownFileIsRefused is the bypass this route would otherwise have.
//
// A `.md` file in a content root is a page, and a page goes through
// `content.Renderer`: redact, parse, render, sanitise. Serving the source from
// `/assets/` would skip every one of those steps, and with them S-5.7's ordering —
// the bytes would reach a reader having passed no redactor at all. Phase 10's
// `[!secret]` callouts would be published in full to anyone who asked for the file.
func TestAMarkdownFileIsRefused(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", []byte("---\ntitle: A page\n---\n[!secret]- GM only\n"))

	recorder := fixed.get("/c/greyhaven/assets/Vault.md", gmRequestor())

	if recorder.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403; a page's source must not bypass the render pipeline",
			recorder.Code)
	}

	if strings.Contains(recorder.Body.String(), "GM only") {
		t.Error("the response carries the page source, including its callout")
	}
}

// TestAScriptIsRefused covers the remaining entries in the refused set, and it is
// worth its own test because a script served from this origin runs in this origin.
func TestAScriptIsRefused(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"payload.js", "payload.mjs", "payload.cjs", "payload.wasm"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixed := newHarness(t)
			fixed.write(name, []byte("alert(document.cookie)"))

			recorder := fixed.get("/c/greyhaven/assets/"+name, gmRequestor())

			if recorder.Code != http.StatusForbidden {
				t.Errorf(
					"status = %d, want 403; a script served from this origin runs in this origin",
					recorder.Code,
				)
			}

			if strings.Contains(recorder.Body.String(), "document.cookie") {
				t.Error("the response carries the script's bytes")
			}
		})
	}
}

// TestAnUnknownExtensionIsNotFound closes the table.
//
// The closedness is the property: a table with a fallback is a table whose
// guarantee is "whatever `mime` or `DetectContentType` says about the bytes", and
// both of those read attacker-controlled content.
func TestAnUnknownExtensionIsNotFound(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("archive.bin", []byte{0x00, 0x01, 0x02})
	fixed.write("noextension", []byte("contents"))
	fixed.write("trailing.", []byte("contents"))

	for _, name := range []string{"archive.bin", "noextension", "trailing."} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			recorder := fixed.get("/c/greyhaven/assets/"+name, gmRequestor())

			if recorder.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404 for an extension the table has no opinion on",
					recorder.Code)
			}

			if strings.Contains(recorder.Body.String(), "contents") {
				t.Error("the response carries the file's bytes")
			}
		})
	}
}

// TestADirectoryIsNotAnAsset covers the non-regular-file check, which is also what
// keeps a named pipe from parking a request on a read forever.
//
// The fixture is a directory whose *name* carries a servable extension, and that is
// the whole point of the fixture. A plain directory name has no extension, so the
// closed table refuses it first and this test would pass with the regular-file check
// deleted — a test that passes for the wrong reason, which is the shape AGENTS.md
// warns about and the reason the extension here is deliberate.
//
// Without the check, `ServeContent` seeks a directory handle to its end, reports the
// directory's size as `Content-Length`, and answers 200 with no bytes: a response
// that claims a representation it did not send.
func TestADirectoryIsNotAnAsset(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("images/map.png", mapBody())

	if err := os.MkdirAll(
		filepath.Join(fixed.dirs[testSlug], filepath.FromSlash("atlas.png")), 0o700,
	); err != nil {
		t.Fatalf("create the directory fixture: %v", err)
	}

	for _, target := range []string{"/c/greyhaven/assets/atlas.png", "/c/greyhaven/assets/images"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()

			recorder := fixed.get(target, gmRequestor())

			if recorder.Code != http.StatusNotFound {
				t.Errorf("a directory answered %d, want 404; body: %d bytes",
					recorder.Code, recorder.Body.Len())
			}
		})
	}
}

// --- The name policies -------------------------------------------------------

// TestHiddenNamesAreNotServed is the index's policy applied here, and the two
// agreeing is the point.
//
// `content`'s index refuses a page whose path has a dot-prefixed component or a
// `node_modules` component, and its debouncer additionally ignores `.tmp`, `.swp`,
// `.swo`, `.swx`, `.part`, `.crdownload` and `~`. Those are spellings other
// programs choose *while writing a file*: `.git/`, `.obsidian/`, `.trash/`, this
// process's own `.semiplane-….tmp` staging file, a browser's partial download.
//
// A file this route serves but the index has no row for is a file with two answers
// to "is this campaign's content", and the asset route's answer is the one a sync
// client can change without a page changing. So the names are refused here too.
//
// The mechanism differs by row and the table says which, because "the test passes"
// is not the same as "the rule I think is doing the work is doing the work": the
// dot cases are refused by the hidden-name policy, and the `.tmp` / `.swp` / `~`
// cases by the closed table, since each of those suffixes *is* the extension of the
// name it applies to. Neither rule is asserted in isolation here — the hidden-name
// mutation is caught by the dot rows and the table mutation by the suffix rows, and
// both mutations are in the mutation report.
func TestHiddenNamesAreNotServed(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		".env",
		".obsidian/workspace.json",
		".git/config",
		"notes/.hidden.png",
		"map.png.tmp",
		"map.png.swp",
		"map.png.swo",
		"map.png.swx",
		"map.png.part",
		"map.png.crdownload",
		"map.png~",
		"node_modules/left-pad/map.png",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixed := newHarness(t)
			fixed.write(name, []byte("SENTINEL-BYTES"))

			recorder := fixed.get("/c/greyhaven/assets/"+name, gmRequestor())

			if recorder.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404 for a name the index also refuses", recorder.Code)
			}

			if strings.Contains(recorder.Body.String(), "SENTINEL-BYTES") {
				t.Error("the response carries the file's bytes")
			}
		})
	}
}

// TestAStableFileWithATransientSuffixIsServed is the other direction, because a
// suffix list with no boundary is a denylist.
//
// `map.tmp.png` is a page whose author has unfortunate taste, not a file being
// written — architecture §5.2's rule is that these are *suffixes* because every one
// of them is a prefix of the page name. A rule that matched anywhere in the name
// would refuse it, and would be refusing a legitimate file.
func TestAStableFileWithATransientSuffixIsServed(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("map.tmp.png", []byte("contents"))

	recorder := fixed.get("/c/greyhaven/assets/map.tmp.png", gmRequestor())

	if recorder.Code != http.StatusOK {
		t.Errorf(
			"status = %d, want 200; the suffix rule matches the end of the name, not the middle",
			recorder.Code,
		)
	}
}

// TestANameEndingInTildeIsRefused: `~` is a suffix with no dot, and a file whose
// last character is a tilde is a backup left by an editor. Named apart because the
// entry is the only one that is not a dotted suffix, so it is the one a future edit
// would most plausibly "fix" into `strings.HasPrefix(name, "~")`.
func TestANameEndingInTildeIsRefused(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("vault.md~", []byte("SENTINEL-BYTES"))

	recorder := fixed.get("/c/greyhaven/assets/vault.md~", gmRequestor())

	if recorder.Code == http.StatusOK {
		t.Fatal("an editor backup was served")
	}

	if strings.Contains(recorder.Body.String(), "SENTINEL-BYTES") {
		t.Error("the response carries the file's bytes")
	}
}

// --- Confinement -------------------------------------------------------------

// TestPathConfinementIsOsRootAndNotAStringCheck is S-3.5, as a table.
//
// Every case is a way a path can leave a campaign, and each must be refused *and*
// must not return a byte of the target. The second half is the half that matters:
// "answered 404" is not the claim, "the bytes of the file outside the root never
// appeared" is. A test that asserts only the status passes just as well against a
// handler that opened `/etc/passwd`, stat'd it, and then refused.
//
// The escaped spellings are the ones that reach a handler at all: `net/http` cleans
// the request path and answers a redirect for a literal `..` before any handler
// runs, so only the percent-encoded forms get here — which is exactly why the
// confinement is load-bearing rather than a second opinion. That is asserted
// separately below, because a case answered by a redirect proves nothing about
// confinement and would hide a broken one.
func TestPathConfinementIsOsRootAndNotAStringCheck(t *testing.T) {
	t.Parallel()

	// Outside the campaign's root, with content no response may contain.
	outside := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(outside, "secret.txt"),
		[]byte(OUTSIDE_SENTINEL),
		0o600,
	); err != nil {
		t.Fatalf("write the outside file: %v", err)
	}

	for _, testCase := range []struct {
		name string
		path string
		want int
	}{
		{
			// An escape the lexical normaliser would catch. Present so the table's
			// interesting cases are visible beside the easy one.
			name: "a dotted parent escape", path: "%2e%2e%2f%2e%2e%2fsecret.txt", want: 403,
		},
		{
			name: "an encoded parent escape", path: "..%2f..%2fsecret.txt", want: 403,
		},
		{
			name: "a mixed parent escape", path: "%2e%2e/..%2fsecret.txt", want: 403,
		},
		{
			// A leading slash names the host's filesystem root, not the campaign's.
			// `Root.At` refuses it as a malformed reference, which is a 400 rather
			// than a 403: it never named a location relative to the root at all.
			name: "an absolute path", path: "%2fetc%2fpasswd", want: 400,
		},
		{
			// A NUL truncates a path for every C library the kernel calls into. The
			// content layer refuses it as malformed rather than letting `os.Root`
			// report it as an escape, because it is a bug-shaped input.
			name: "a NUL byte", path: "map%00.png", want: 400,
		},
		{
			// An escape by a symlink out of the tree. S-4.4 refuses every symlink,
			// and `os.Root` refuses this one independently — the policy is the second
			// layer, not the only one.
			name: "a symlink pointing out of the root", path: "escape.txt", want: 403,
		},
		{
			// A symlink that stays inside the root, which is the case the *policy*
			// refuses rather than the confinement. A link is a second name for a file,
			// and a second name is an identity nothing indexed.
			name: "a symlink pointing inside the root", path: "inside-link.txt", want: 403,
		},
		{
			// A Windows separator, which is the traversal `filepath`-based code on
			// Windows would follow and which a lexical check written against
			// `path.Clean` does not see at all. No status is asserted and the reason
			// is worth stating: on Linux the string is one filename component that
			// begins with a dot, so the name policy refuses it as a 404, and on Windows
			// it is a traversal. The *refusal* is the same on both, and the status is
			// a function of the platform a deployment runs on rather than of the
			// request — which is exactly the property that makes asserting it here
			// wrong.
			name: "a windows-style parent", path: "..%5c..%5csecret.txt",
		},
		{
			// Long enough that the kernel refuses the lookup. Refused either way, and
			// the status is not asserted: ENAMETOOLONG is not portable.
			name: "a very long path", path: strings.Repeat("a", 300) + "/" + strings.Repeat("b", 300),
		},
		{
			name: "a very long component", path: strings.Repeat("c", 500) + ".png",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			fixed := newHarness(t)
			fixed.write("map.png", mapBody())

			// A file inside the root at the exact spelling of the escape target, so a
			// handler that resolved the escape to *inside* the root would be caught
			// too rather than looking like a successful refusal.
			fixed.write("secret.txt", []byte(INSIDE_SENTINEL))

			if testCase.name == "a symlink pointing out of the root" {
				fixed.symlink(filepath.Join(outside, "secret.txt"), "escape.txt")
			}

			if testCase.name == "a symlink pointing inside the root" {
				fixed.symlink("secret.txt", "inside-link.txt")
			}

			recorder := fixed.get("/c/greyhaven/assets/"+testCase.path, gmRequestor())

			if testCase.want != 0 && recorder.Code != testCase.want {
				t.Errorf("status = %d, want %d", recorder.Code, testCase.want)
			}

			if recorder.Code == http.StatusOK {
				t.Errorf("status = 200; the path was served")
			}

			// The substantive half. A refusal that echoed a byte of the target would
			// already have leaked it.
			for _, sentinel := range []string{OUTSIDE_SENTINEL, INSIDE_SENTINEL} {
				if strings.Contains(recorder.Body.String(), sentinel) {
					t.Errorf(
						"the response contains %q; a refusal that returns a byte of the target "+
							"is not a refusal",
						sentinel,
					)
				}
			}
		})
	}
}

// OUTSIDE_SENTINEL is the content of a file outside the campaign's root. No response
// may contain it, ever.
const OUTSIDE_SENTINEL = "OUTSIDE-THE-CAMPAIGN-ROOT"

// INSIDE_SENTINEL is the content of a file *inside* the campaign's root, placed at
// the spelling several escape attempts resolve to. It must not appear either: a
// handler that resolved `../secret.txt` to `secret.txt` has confused "refused" with
// "clamped", and both look like a 404.
const INSIDE_SENTINEL = "INSIDE-THE-CAMPAIGN-ROOT"

// symlink creates a link inside the campaign's root.
func (h *harness) symlink(target, name string) {
	h.t.Helper()

	full := filepath.Join(h.dirs[testSlug], filepath.FromSlash(name))
	if err := os.Symlink(target, full); err != nil {
		h.t.Fatalf("symlink %s -> %s: %v", name, target, err)
	}
}

// TestALiteralParentSegmentIsAnsweredByTheMuxRatherThanTheHandler records the
// other half of the escaping interaction, in both directions.
//
// A literal `..` never reaches a handler: `net/http` cleans the request path and
// answers a redirect. A handler that answered it would be answering a URL no client
// sends. Asserting the redirect *and* the absence of the file's bytes is what makes
// it a confinement claim rather than a redirect claim.
func TestALiteralParentSegmentIsAnsweredByTheMuxRatherThanTheHandler(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("notes/map.png", mapBody())
	fixed.write("secret.txt", []byte(INSIDE_SENTINEL))

	recorder := fixed.get("/c/greyhaven/assets/notes/../secret.txt", gmRequestor())

	if recorder.Code != http.StatusTemporaryRedirect {
		t.Errorf("status = %d, want 307 from the mux's own path cleaning", recorder.Code)
	}

	if strings.Contains(recorder.Body.String(), INSIDE_SENTINEL) {
		t.Error("the redirect carried the target's bytes")
	}
}

// TestTheConfinementHoldsThroughANullByteInEveryPosition is the NUL case from the
// other direction: a NUL is only interesting where a string is truncated *after* a
// check. `Root.At` refuses it, and the refusal is a 400 because the input never
// named a location.
//
// Named apart from the table's `map%00.png` because the position matters: this one
// truncates a *valid-looking* name, so a handler that checked the extension before
// the NUL would classify `map.png\0` as a PNG.
func TestTheConfinementHoldsThroughANullByteInEveryPosition(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("map.png", mapBody())

	for _, path := range []string{
		"map.png%00.png",
		"map%00/notes.png",
		"%00map.png",
		"notes/map.png%00",
	} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			recorder := fixed.get("/c/greyhaven/assets/"+path, gmRequestor())

			if recorder.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", recorder.Code)
			}

			if strings.Contains(recorder.Body.String(), "AAAAAAAAAA") {
				t.Error("the response carries the asset's bytes")
			}
		})
	}
}

// TestTheRouteRefusesAPathWithNoName is the empty `{path...}`: the route matched
// `/c/{slug}/assets/` with nothing after it. A path that names nothing is malformed
// rather than absent.
func TestTheRouteRefusesAPathWithNoName(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)

	recorder := fixed.get("/c/greyhaven/assets/", gmRequestor())

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", recorder.Code)
	}
}

// TestACampaignWithNoContentRootIsAFaultAndNotASilent404 is the unclassified-error
// default, asserted rather than assumed.
//
// `content.ErrNoRoot` means the campaign row exists and its directory does not,
// which S-4.5 marks *degraded*: the server keeps running and the operator has to
// hear about it. So it is a 500, not a 404, even though a 404 would be the more
// conservative answer for the reader. Two reasons, and they are the same reason:
// `classify`'s default is the 500 so that an error nobody taught it about cannot
// become "there is nothing at this path", and a 404 would be logged at debug where
// nobody reads it — a campaign quietly serving nothing for a week is the failure
// this must not have.
//
// The reader sees the same designed state either way. What differs is the log level,
// and that is where the operator is.
func TestACampaignWithNoContentRootIsAFaultAndNotASilent404(t *testing.T) {
	t.Parallel()

	registry := content.NewRegistry(content.RefuseSymlinks)
	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Errorf("close content root: %v", err)
		}
	})

	campaignMux := http.NewServeMux()
	assets.Mount(campaignMux, &assets.Handler{Roots: registry})

	outer := http.NewServeMux()
	outer.Handle("/c/{slug}/",
		campaigns.Resolve(campaignStore{
			campaigns: map[string]domain.Campaign{
				testSlug: {ID: testCampID, Slug: testSlug, Visibility: domain.VisibilityPublic},
			},
			members: map[int64]domain.Role{},
		})(
			middleware.Chain(campaignMux, campaigns.RequireRead),
		),
	)

	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet, "/c/greyhaven/assets/map.png", http.NoBody,
	)
	req = req.WithContext(identity.WithRequestor(req.Context(), anonymousRequestor()))

	recorder := httptest.NewRecorder()
	outer.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500; a degraded campaign must be a fault somebody hears "+
			"about, not a 404 logged at debug", recorder.Code)
	}
}

// TestACampaignWhoseRootCannotBeFoundIsNotFound is the same for a slug the
// registry has never heard of, which is the shape a renamed campaign takes while
// the process is running.
func TestACampaignWhoseRootCannotBeFoundIsNotFound(t *testing.T) {
	t.Parallel()

	registry := content.NewRegistry(content.RefuseSymlinks)
	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Errorf("close content root: %v", err)
		}
	})

	campaignMux := http.NewServeMux()
	assets.Mount(campaignMux, &assets.Handler{Roots: registry})

	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet, "/c/greyhaven/assets/map.png", http.NoBody,
	)
	req = req.WithContext(identity.WithRequestor(req.Context(), anonymousRequestor()))

	recorder := httptest.NewRecorder()
	campaignMux.ServeHTTP(recorder, req)

	if recorder.Code == http.StatusOK {
		t.Error("an asset was served with no content root at all")
	}
}

// --- Gating, headers and the failure document --------------------------------

// TestTheRefusalOfAnUnknownCampaignComesFromTheGateAndNotFromThisRoute is ADR
// 0024, asserted from the shape of the refusal.
//
// Authorisation is a gate the route mounts, never a check inside a handler, and the
// consequence is that this handler must *not* be able to refuse a reader on its own:
// `campaigns.AccessFrom` returns `TierNone` for a request that never passed
// `Resolve`, and a handler that branched on that would be a second copy of the S-8
// matrix. What proves the gate is upstream is not the status — this route answers
// 403 and 404 of its own — but the *body*: `campaigns.Guard` writes
// `{"error":"not found"}` as JSON, and this route's own failures are the designed
// shell state. An unknown campaign gets the former, so the handler never ran.
//
// Asserting the status alone would pass against a handler that also checked the
// tier, which is the change this rule forbids.
func TestTheRefusalOfAnUnknownCampaignComesFromTheGateAndNotFromThisRoute(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("map.png", mapBody())

	recorder := fixed.get("/c/no-such-campaign/assets/map.png", gmRequestor())

	if recorder.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", recorder.Code)
	}

	if got := recorder.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want the gate's JSON; this route's own failures are the "+
			"designed shell state, so anything else means the handler refused the request itself "+
			"and is therefore carrying its own copy of the access matrix", got)
	}

	if got := recorder.Body.String(); got != "{\"error\":\"not found\"}\n" {
		t.Errorf("body = %q, want the gate's not-found body", got)
	}
}

// TestAMissingCampaignRefusesTheAssetRatherThanTheGate is the other shape: a
// campaign the reader *is* a member of, whose asset does not exist, must get this
// route's designed state rather than the gate's plain 404. It is the pair of the
// test above, and the pair is the point — the two refusals are distinguishable
// because they are different facts about different things, and a reader who cannot
// tell them apart cannot report a broken link.
func TestAMissingCampaignRefusesTheAssetRatherThanTheGate(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("map.png", mapBody())

	recorder := fixed.get("/c/greyhaven/assets/absent.png", gmRequestor())

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}

	if got := recorder.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want the designed shell state", got)
	}
}

// TestTheCampaignMountMustCarryTheSlug guards the one thing about `router.go` this
// route depends on and cannot fix itself.
//
// `campaigns.Resolve` reads the campaign out of `r.PathValue("slug")`, and `net/http`
// sets a path value from the pattern that matched — not from the mux the handler
// underneath happens to be. Mounted at `/c/`, no wildcard is named, so Resolve
// resolves nothing, the tier stays TierNone, and RequireRead answers 404 for every
// request under `/c/`, including an asset that exists.
//
// Asserted both ways on purpose: the correct mount serves the asset, and the
// prefix-only mount does not. A test that only asserted the first would still pass
// after somebody "simplified" the mount back, which is the change that breaks it.
func TestTheCampaignMountMustCarryTheSlug(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("map.png", mapBody())

	req := func() *http.Request {
		request := httptest.NewRequestWithContext(
			t.Context(), http.MethodGet, "/c/greyhaven/assets/map.png", http.NoBody,
		)

		return request.WithContext(identity.WithRequestor(request.Context(), gmRequestor()))
	}

	withSlug := httptest.NewRecorder()
	fixed.serveMountedAt("/c/{slug}/").ServeHTTP(withSlug, req())

	if withSlug.Code != http.StatusOK {
		t.Errorf(
			"the /c/{slug}/ mount answered %d, want 200; body:\n%s",
			withSlug.Code,
			withSlug.Body,
		)
	}

	prefixOnly := httptest.NewRecorder()
	fixed.serveMountedAt("/c/").ServeHTTP(prefixOnly, req())

	if prefixOnly.Code != http.StatusNotFound {
		t.Errorf("the /c/ mount answered %d, want 404; it cannot see {slug}, so campaigns.Resolve "+
			"resolves nothing and RequireRead refuses every request", prefixOnly.Code)
	}
}

// TestEveryResponseVariesOnTheCookie is the corrected ADR 0035 applied here, on
// every status this route can produce.
//
// Asserted per status because a `Vary` written on one path and forgotten on another
// is indistinguishable from a correct one until a cache merges the two, which is
// the failure it exists to prevent. A 404 written for one reader and served to
// another is a 404 for a file that exists.
func TestEveryResponseVariesOnTheCookie(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t).private()
	fixed.write("map.png", mapBody())
	fixed.write("page.html", []byte("<html></html>"))
	fixed.write(".hidden.png", []byte("contents"))

	for _, testCase := range []struct {
		name   string
		target string
		header http.Header
	}{
		{name: "a 200", target: "/c/greyhaven/assets/map.png"},
		{
			name: "a 206", target: "/c/greyhaven/assets/map.png",
			header: http.Header{"Range": {"bytes=0-9"}},
		},
		{name: "a 400", target: "/c/greyhaven/assets/map%00.png"},
		{name: "a 403", target: "/c/greyhaven/assets/page.html"},
		{name: "a 404", target: "/c/greyhaven/assets/.hidden.png"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			recorder := fixed.getWith(testCase.target, gmRequestor(), testCase.header)

			if got := recorder.Header().Get("Vary"); got != "Cookie" {
				t.Errorf("Vary = %q on a %d, want %q", got, recorder.Code, "Cookie")
			}
		})
	}
}

// TestTheFailureDocumentEchoesNothingFromTheRequest is the refusal rule, as a
// table over the inputs a reader might think are worth naming back.
//
// `content.Root` refuses to put the offending path in its error because an error
// whose text varies with the caller's input is a channel. The failure document is
// the largest thing on the page, so a heading that echoed the path would undo that
// one string later — and on this route the string is a path *inside a directory the
// request named*, which is what makes it worth asserting rather than assuming.
func TestTheFailureDocumentEchoesNothingFromTheRequest(t *testing.T) {
	t.Parallel()

	for _, target := range []string{
		"/c/greyhaven/assets/notes/%2e%2e%2f%2e%2e%2fetc%2fpasswd",
		"/c/greyhaven/assets/map%00.png",
		"/c/greyhaven/assets/page.html",
		"/c/greyhaven/assets/.hidden.png",
		"/c/greyhaven/assets/archive.bin",
	} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()

			fixed := newHarness(t)
			fixed.write("page.html", []byte("<html></html>"))
			fixed.write(".hidden.png", []byte("contents"))
			fixed.write("archive.bin", []byte{0x00})

			recorder := fixed.get(target, gmRequestor())

			if recorder.Code == http.StatusOK {
				t.Fatalf("status = 200; %s was served", target)
			}

			for _, absent := range []string{"passwd", "%2e", "..", "notes", "hidden", "archive"} {
				if strings.Contains(recorder.Body.String(), absent) {
					t.Errorf("the failure document contains %q, which came from the request:\n%s",
						absent, recorder.Body.String())
				}
			}
		})
	}
}

// TestTheFailureDocumentIsTheDesignedStateAndNotBareText is the sibling route's
// rule, and it is why the failure is rendered rather than returned as a status.
//
// A reader who reaches here passed a gate, so they are a member of this campaign or
// a reader of a public one; `net/http`'s own 404 is plain text with no shell around
// it and no reference in it, and a GM who mistyped a link needs to be able to give
// the reader's report to somebody. Parsed with `golang.org/x/net/html` rather than
// matched as a string, because a substring assertion over markup is how a gate
// passes while the thing it watches sits in a comment or an attribute.
func TestTheFailureDocumentIsTheDesignedStateAndNotBareText(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("map.png", mapBody())

	recorder := fixed.get("/c/greyhaven/assets/absent.png", gmRequestor())

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}

	document, err := html.Parse(bytes.NewReader(recorder.Body.Bytes()))
	if err != nil {
		t.Fatalf("the failure document is not parseable HTML: %v", err)
	}

	for _, testCase := range []struct {
		selector string
		why      string
	}{
		{`[data-testid="error-state"]`, "the load-error state is not present"},
		{`main`, "there is no main landmark"},
		{`h1`, "there is no heading naming what failed"},
		{`[data-testid="error-state-reference"]`, "the reference a reader can quote is absent"},
	} {
		if count := countMatching(document, testCase.selector); count == 0 {
			t.Errorf("%s (%s)", testCase.why, testCase.selector)
		}
	}

	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q on a failure, want %q", got, "no-store")
	}
}

// TestTheFailureDocumentNamesNoRetiredEntity is UI §1.2 and §10.2, and this route's
// copy is its own.
//
// "World" and "session" appear nowhere in the interface, and the state package is
// where the wording lives — so the audit belongs here rather than only in the
// shell's own tests, because a route can put a string in front of a reader that the
// state package never sees. Checked on the parsed tree, over text *and*
// attributes: a substring scan over the raw bytes would miss a `title` attribute and
// would match the word inside a comment nobody renders.
func TestTheFailureDocumentNamesNoRetiredEntity(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("map.png", mapBody())

	recorder := fixed.get("/c/greyhaven/assets/absent.png", gmRequestor())

	document, err := html.Parse(bytes.NewReader(recorder.Body.Bytes()))
	if err != nil {
		t.Fatalf("the failure document is not parseable HTML: %v", err)
	}

	assertNoRetiredEntity(t, document, recorder.Body.String())
}

// TestTheRouteIsGetOnly rejects a write to an asset URL.
//
// The mux registers `GET`, which in Go 1.22 also covers `HEAD`. A `PUT` matches no
// pattern and falls through to the router's own 404, and this asserts it — a route
// that also matched `PUT` would be a second write path with none of S-6.2's
// `If-Match` discipline.
func TestTheRouteIsGetOnly(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("map.png", mapBody())

	recorder := fixed.request(http.MethodPut, "/c/greyhaven/assets/map.png", gmRequestor(), nil)

	// `net/http` answers 405 for a method the pattern does not carry, and the
	// router's own not-found handler answers 404 for a pattern that does not match.
	// Either is a refusal; what is not acceptable is a 200, because a route that
	// matched `PUT` would be a second write path with none of S-6.2's `If-Match`
	// discipline behind it.
	if recorder.Code != http.StatusMethodNotAllowed &&
		recorder.Code != http.StatusNotFound {
		t.Errorf("a PUT to an asset URL answered %d, want 405 or 404", recorder.Code)
	}

	if strings.Contains(recorder.Body.String(), "AAAAAAAAAA") {
		t.Error("the response carries the asset's bytes")
	}
}

// TestAHeadRequestCarriesTheHeadersAndNoBody is the method the mux registers
// alongside `GET`, and it matters for a map: a client asks for the size before it
// downloads.
func TestAHeadRequestCarriesTheHeadersAndNoBody(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("map.png", mapBody())

	recorder := fixed.request(http.MethodHead, "/c/greyhaven/assets/map.png", gmRequestor(), nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	if recorder.Body.Len() != 0 {
		t.Errorf("a HEAD carried %d bytes of body", recorder.Body.Len())
	}

	for header, want := range map[string]string{
		"Content-Length": "1024",
		"Content-Type":   "image/png",
		"Accept-Ranges":  "bytes",
	} {
		if got := recorder.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

// TestAnEmptyAssetIsServedAndItsRangeIsIgnored is the empty-file edge case, and it
// is in the standard library's range logic rather than in this route's.
//
// RFC 9110 §14.1.1 cannot express a range of a zero-length representation, so a
// server asked for one either fails or ignores it. `ServeContent` ignores it and
// sends the whole (empty) representation, which is the answer that does not break a
// client asking for "the last 1 MB" of an empty placeholder file.
func TestAnEmptyAssetIsServedAndItsRangeIsIgnored(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("placeholder.png", []byte{})

	recorder := fixed.get("/c/greyhaven/assets/placeholder.png", gmRequestor())

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	if got := recorder.Header().Get("Content-Length"); got != "0" {
		t.Errorf("Content-Length = %q, want %q", got, "0")
	}

	ranged := fixed.rangeGet("/c/greyhaven/assets/placeholder.png", "bytes=0-99")

	if ranged.Code != http.StatusOK {
		t.Errorf("a ranged request for an empty asset answered %d, want 200", ranged.Code)
	}
}

// TestConcurrentRequestsForOneAssetAllSucceed is the handler's concurrency claim
// made real.
//
// Every field is written once before the server starts and read on every request,
// so a `Handler` is safe for concurrent use — and the way to know that is not the
// doc comment but sixteen readers of the same file arriving at once, with a race
// detector running.
func TestConcurrentRequestsForOneAssetAllSucceed(t *testing.T) {
	t.Parallel()

	body := mapBody()

	fixed := newHarness(t)
	fixed.write("images/map.png", body)

	const readers = 16

	results := make([]*httptest.ResponseRecorder, readers)

	var group sync.WaitGroup

	for i := range readers {
		// `Go` adds to the group itself; a second `Add` would leave the counter one
		// above zero and `Wait` would block for ever.
		group.Go(func() {
			results[i] = fixed.get("/c/greyhaven/assets/images/map.png", gmRequestor())
		})
	}

	group.Wait()

	validator := ""
	for i, recorder := range results {
		if recorder.Code != http.StatusOK {
			t.Fatalf("reader %d got %d, want 200", i, recorder.Code)
		}

		if recorder.Body.String() != string(body) {
			t.Errorf("reader %d got %d bytes, want %d", i, recorder.Body.Len(), len(body))
		}

		if validator == "" {
			validator = recorder.Header().Get("ETag")
		}

		if recorder.Header().Get("ETag") != validator {
			t.Errorf(
				"reader %d got validator %q, want %q",
				i,
				recorder.Header().Get("ETag"),
				validator,
			)
		}
	}
}

// --- helpers for the DOM assertions ------------------------------------------

// countMatching counts the nodes matching a selector.
//
// Written against a selector list rather than a hand-rolled walk so that the
// assertion is about the document a reader gets and not about how this file happens
// to traverse it.
func countMatching(root *html.Node, selector string) int {
	matches, err := querySelectorAll(root, selector)
	if err != nil {
		// The selectors here are literals in this file, so a parse failure is a bug
		// in the test rather than a condition to tolerate.
		panic(err)
	}

	return len(matches)
}

// assertNoRetiredEntity fails on any occurrence of a retired noun in the document's
// text or in any attribute value.
//
// Comments are deliberately *included*: a reader never sees a comment, which is
// exactly why a scan that excluded them would pass while the word sat in the
// document.
func assertNoRetiredEntity(t *testing.T, document *html.Node, raw string) {
	t.Helper()

	for _, node := range walkNodes(document) {
		var candidate string

		switch node.Type {
		case html.TextNode, html.CommentNode:
			candidate = node.Data
		default:
			values := make([]string, 0, len(node.Attr))
			for _, attr := range node.Attr {
				values = append(values, attr.Val)
			}

			candidate = strings.Join(values, " ")
		}

		lowered := strings.ToLower(candidate)
		for _, word := range []string{"world", "session"} {
			if strings.Contains(lowered, word) {
				t.Errorf("the document names %q:\n%s", word, raw)
			}
		}
	}
}

// walkNodes returns every node under root, root included.
func walkNodes(root *html.Node) []*html.Node {
	var out []*html.Node

	var walk func(current *html.Node)

	walk = func(current *html.Node) {
		out = append(out, current)

		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(root)

	return out
}

// querySelectorAll is the selector evaluation the DOM assertions use.
//
// `golang.org/x/net/html` has no CSS selector engine, so the selectors in this file
// are limited to the two forms the assertions need — a bare tag name and an
// attribute-presence test — and anything else is a parse error rather than a silent
// miss. A selector engine that quietly matched nothing would make every assertion
// below vacuous, which is the failure mode this project has been bitten by before.
func querySelectorAll(root *html.Node, selector string) ([]*html.Node, error) {
	switch {
	case strings.HasPrefix(selector, "["):
		return attributeMatches(root, selector)
	case selector == "" || strings.ContainsAny(selector, " .#"):
		return nil, fmt.Errorf("assets_test: unsupported selector %q", selector)
	default:
		return nodesWithTag(root, selector), nil
	}
}

// attributeMatches evaluates an attribute selector: `[name]` for presence and
// `[name="value"]` for an exact value.
//
// Two forms and no more, because a selector engine that quietly matched nothing
// would make every assertion using it vacuous — and a vacuous gate is worse than no
// gate, because it is a green light wired to nothing. Anything outside these two
// forms is an error this file fails loudly on.
func attributeMatches(root *html.Node, selector string) ([]*html.Node, error) {
	body := strings.TrimSuffix(strings.TrimPrefix(selector, "["), "]")

	name, value, hasValue := strings.Cut(body, "=")
	if !hasValue {
		return nodesWithAttribute(root, name), nil
	}

	return nodesWithAttributeValue(root, name, strings.Trim(value, `"'`)), nil
}

// nodesWithAttributeValue returns every node whose named attribute has the value.
func nodesWithAttributeValue(root *html.Node, attribute, want string) []*html.Node {
	var out []*html.Node

	for _, node := range walkNodes(root) {
		for _, attr := range node.Attr {
			if attr.Key == attribute && attr.Val == want {
				out = append(out, node)

				break
			}
		}
	}

	return out
}

// nodesWithAttribute returns every node carrying the named attribute.
func nodesWithAttribute(root *html.Node, attribute string) []*html.Node {
	var out []*html.Node

	for _, node := range walkNodes(root) {
		for _, attr := range node.Attr {
			if attr.Key == attribute {
				out = append(out, node)

				break
			}
		}
	}

	return out
}

// nodesWithTag returns every element with the given tag name.
func nodesWithTag(root *html.Node, tag string) []*html.Node {
	var out []*html.Node

	for _, node := range walkNodes(root) {
		if node.Type == html.ElementNode && node.Data == tag {
			out = append(out, node)
		}
	}

	return out
}
