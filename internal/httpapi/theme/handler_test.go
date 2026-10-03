package theme_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/httpapi/identity"
	"github.com/semiplane/semiplane/internal/httpapi/middleware"
	"github.com/semiplane/semiplane/internal/httpapi/theme"
	"github.com/semiplane/semiplane/internal/store"
)

// The route: `GET /c/{slug}/theme.css`.
//
// # The harness builds the chain `router.go` builds
//
// `campaignMux` is the one `mountCampaignRoutes` fills; `Resolve` sits outside
// `RequireRead`; the whole thing is mounted at `/c/{slug}/` behind `RequestID`. The
// chain is assembled here rather than borrowed from the router because
// `router.go` is the integrator's file and this route is not mounted yet — and the
// arrangement is the arrangement, so a change on either side shows up as a failing
// test rather than as a route that quietly lost its gate.
//
// Two campaigns, because "a cache entry is never shared across campaigns" (S-8.3)
// is a claim about two of them and a test with one cannot make it. Both roots are
// real `os.Root`s held by one registry, so a manifest that resolves in one campaign
// resolves there and nowhere else.

// The campaigns these tests read, and the identifiers their rows carry.
const (
	testSlug    = "greyhaven"
	otherSlug   = "saltmarsh"
	testCampID  = int64(1)
	otherCampID = int64(2)
	gmUser      = int64(10)
	playerUser  = int64(11)
)

// sheetFile is the route's own path segment, and the one thing this route serves.
//
// Deliberately *not* `theme.ManifestFileName`: the manifest is `theme.yaml` and the
// route is `theme.css`, and a test that reached for the manifest constant to build
// the sheet's URL would pass only because the two happened to be spelled similarly
// in some other route's tests.
const sheetFile = "theme.css"

// themePath is the route's URL for the test campaign.
var themePath = "/c/" + testSlug + "/" + sheetFile

// The three readers a public campaign admits, and the tier each resolves to.
//
// Anonymous is not a curiosity here: it is the reader whose presence in the
// byte-identity matrix is the point, because a per-*user* theme would first show up
// as "the signed-in readers get something different".
func gmRequestor() domain.Requestor {
	return domain.Requestor{Authenticated: true, UserID: gmUser, Username: "mira"}
}

func playerRequestor() domain.Requestor {
	return domain.Requestor{Authenticated: true, UserID: playerUser, Username: "tobin"}
}

func anonymousRequestor() domain.Requestor {
	return identity.Anonymous()
}

// outsiderRequestor is signed in and a member of nothing, which S-8 resolves to
// TierNone for a private campaign — the reader a private campaign's existence must
// be invisible to.
func outsiderRequestor() domain.Requestor {
	return domain.Requestor{Authenticated: true, UserID: 99, Username: "wren"}
}

// readers is every reader the gate admits for a public campaign.
func readers() map[string]domain.Requestor {
	return map[string]domain.Requestor{
		"anonymous": anonymousRequestor(),
		"player":    playerRequestor(),
		"gm":        gmRequestor(),
	}
}

// uiPreferences are the `sp_ui` cookie values the head resolver can be handed.
//
// Five, and that number is ADR 0035's: the document's independence from this cookie
// is held by asserting the *bytes* are identical across it, and five is the set that
// record used. The values span both axes the cookie carries — the theme and the mode
// — plus the two the resolver treats specially, so a handler that read either field
// would produce a different answer for at least one of them.
//
// `theme=auto` and a missing `ui` field are in here because they are what the
// resolver itself writes when a visitor has expressed no preference, so they are the
// values a real deployment serves most.
func uiPreferences() []string {
	return []string{
		"theme=auto&ui=laptop",
		"theme=light&ui=phone",
		"theme=dark&ui=tv",
		"theme=dark&ui=laptop",
		"theme=auto",
	}
}

// campaignStore answers the two queries the access gate reads and refuses the three
// it does not need.
type campaignStore struct {
	campaigns map[string]domain.Campaign
	members   map[int64]domain.Role
	visible   bool
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

// harness is one assembled route and the vaults behind it.
//
// The handler is built once and shared by every request, because it holds the one
// piece of state this route has — each campaign's last good sheet — and a harness
// that rebuilt it per request would make §4.12.3's retention untestable: the second
// request would arrive at a handler that had never seen the first.
type harness struct {
	t          *testing.T
	registry   *content.Registry
	dirs       map[string]string
	visibility domain.Visibility
	logs       *recordingLogger
	handler    *theme.Handler
}

// newHarness builds a route over a public campaign with no manifest in it.
//
// Public by default, deliberately: a gate answering 404 for everything is
// indistinguishable from a route that works, and a suite whose failures are invisible
// is worse than one that is slow. `private()` is one call away.
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

	logs := newRecordingLogger()

	return &harness{
		t:          t,
		registry:   registry,
		dirs:       dirs,
		visibility: domain.VisibilityPublic,
		logs:       logs,
		handler:    &theme.Handler{Roots: registry, Logger: logs.logger()},
	}
}

// private makes the harness's campaign private, so a reader who is not a member is
// refused rather than served.
func (h *harness) private() *harness {
	h.visibility = domain.VisibilityPrivate

	return h
}

// write puts a manifest into the test campaign's content root.
func (h *harness) write(body string) {
	h.t.Helper()

	h.writeIn(testSlug, body)
}

// writeIn is `write` for a named campaign.
func (h *harness) writeIn(slug, body string) {
	h.t.Helper()

	full := filepath.Join(h.dirs[slug], theme.ManifestFileName)
	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		h.t.Fatalf("write %s in %s: %v", theme.ManifestFileName, slug, err)
	}
}

// remove deletes the test campaign's manifest, which is how a campaign withdraws a
// theme.
func (h *harness) remove() {
	h.t.Helper()

	if err := os.Remove(filepath.Join(h.dirs[testSlug], theme.ManifestFileName)); err != nil {
		h.t.Fatalf("remove the manifest: %v", err)
	}
}

// route is the route under test, shared across this harness's requests.
func (h *harness) route() *theme.Handler {
	return h.handler
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
	campaignMux := http.NewServeMux()
	theme.Mount(campaignMux, h.route())

	outer := http.NewServeMux()
	outer.Handle("/c/{slug}/",
		campaigns.Resolve(h.store())(
			middleware.Chain(campaignMux, campaigns.RequireRead),
		),
	)

	return middleware.RequestID(outer)
}

// store builds the campaign rows this harness serves.
func (h *harness) store() campaignStore {
	return campaignStore{
		campaigns: map[string]domain.Campaign{
			testSlug: {
				ID: testCampID, Slug: testSlug, Name: "Greyhaven", Visibility: h.visibility,
			},
			otherSlug: {
				ID: otherCampID, Slug: otherSlug, Name: "Saltmarsh",
				Visibility: domain.VisibilityPublic,
			},
		},
		members: map[int64]domain.Role{
			gmUser:     domain.RoleGM,
			playerUser: domain.RolePlayer,
		},
		visible: true,
	}
}

// get issues one request as requestor.
func (h *harness) get(target string, requestor domain.Requestor) *httptest.ResponseRecorder {
	h.t.Helper()

	return h.getAs(target, requestor, nil)
}

// getAs issues one request carrying headers.
func (h *harness) getAs(
	target string,
	requestor domain.Requestor,
	header http.Header,
) *httptest.ResponseRecorder {
	h.t.Helper()

	req := httptest.NewRequestWithContext(h.t.Context(), http.MethodGet, target, http.NoBody)
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

// TestAStyledSheetIsServed is the ordinary success: a 200, the generated
// declarations, and the headers that say the response is revalidatable and private.
//
// The header assertions are read against the record and the assets route at the same
// time, because both are the reason to change them: `Cache-Control` is
// `private, no-cache` rather than §6.6's `public` (see the deviation note in
// theme.go), `Vary: Cookie` is there for the cache-selection reason that route
// documents, and `nosniff` keeps `ServeContent`'s sniffing branch unreachable.
func TestAStyledSheetIsServed(t *testing.T) {
	t.Parallel()

	served := newHarness(t)
	served.write(manifest(fixtureAccent, fixtureInk))

	recorder := served.get(themePath, gmRequestor())

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body:\n%s", recorder.Code, recorder.Body)
	}

	body := recorder.Body.String()
	for _, want := range []string{brandAccent + ": " + fixtureAccent, brandInk + ": " + fixtureInk} {
		if !strings.Contains(body, want) {
			t.Errorf("the served sheet does not declare %q:\n%s", want, body)
		}
	}

	for header, want := range map[string]string{
		"Content-Type":           "text/css; charset=utf-8",
		"X-Content-Type-Options": "nosniff",
		"Cache-Control":          "private, no-cache",
		"Vary":                   "Cookie",
	} {
		if got := recorder.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}

	etag := recorder.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag; §6.6 makes the validator the whole cache story")
	}

	if !strings.HasPrefix(etag, `"`) || strings.HasPrefix(etag, `W/`) {
		t.Errorf("ETag = %q, want a strong validator in quotes: the generated bytes "+
			"are in hand, so a weak validator would be a claim weaker than the one "+
			"this route can make", etag)
	}
}

// TestTheSheetIsByteIdenticalForEveryReaderAndPreference is the assertion §4.12.4
// and ADR 0035 are about, and it is deliberately the *bytes* rather than a header.
//
// ADR 0035's correction is explicit: "No `Vary` header names `sp_ui`, and the
// document's independence from that cookie is held by asserting the bytes are
// byte-identical across five cookie values, not by asserting the header's absence."
// The reason is general, and this is the case for it: a test asserting that a header
// names nothing cannot see the variation that header was protecting — it only checks
// that we did not *say* we vary. Comparing bodies can only fail if we *do* vary.
//
// So the matrix is every reader the gate admits (anonymous, player, GM) crossed with
// every `sp_ui` value the resolver can be handed, and every one of the fifteen
// responses must be the same bytes **and** the same validator. A per-user theme
// would break this on the first reader that differed; a per-campaign theme passes it
// because there is nothing in the route that could tell the readers apart.
//
// The header checks below this one are the *symptom* assertions, and they are marked
// as such.
func TestTheSheetIsByteIdenticalForEveryReaderAndPreference(t *testing.T) {
	t.Parallel()

	served := newHarness(t)
	served.write(manifest(fixtureAccent, fixtureInk))

	var (
		firstBody string
		firstETag string
		firstFor  string
	)

	for name, requestor := range readers() {
		for _, preference := range uiPreferences() {
			recorder := served.getAs(themePath, requestor,
				http.Header{"Cookie": {"sp_ui=" + preference}})

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d as %s with sp_ui=%s, want 200; body:\n%s",
					recorder.Code, name, preference, recorder.Body)
			}

			label := name + " with sp_ui=" + preference

			if firstBody == "" {
				firstBody, firstETag, firstFor = recorder.Body.String(),
					recorder.Header().Get("ETag"), label

				continue
			}

			if recorder.Body.String() != firstBody {
				t.Errorf("%s received different bytes from %s.\nA campaign theme is a "+
					"property of the campaign (UI §4.12.4): anything that made this "+
					"vary would make the document vary by cookie, which is ADR 0035's "+
					"failure:\n--- %s\n%s\n--- %s\n%s",
					label, firstFor, firstFor, firstBody, label, recorder.Body)
			}

			if got := recorder.Header().Get("ETag"); got != firstETag {
				t.Errorf("%s was served the validator %q and %s was served %q; two "+
					"responses with the same body must advertise the same validator, "+
					"or a cache will treat one as stale",
					label, got, firstFor, firstETag)
			}
		}
	}
}

// TestNoResponseVariesOnTheThemeCookie is the symptom, and it says so.
//
// This is the check ADR 0035 would have written if it had only checked headers, and
// it is kept precisely because it is *cheap* — not because it is the claim. A handler
// that branched on the cookie and emitted `Vary: Cookie` would pass it. The claim is
// the byte-identity above; this only notices if we start announcing a variation.
//
// Two spellings of the cookie are checked because the route's own parser is
// tolerant: the shell writes `&`-separated pairs and the browser may hand them back
// `;`-separated, and a `Vary` naming `sp_ui` would fragment on both.
func TestNoResponseVariesOnTheThemeCookie(t *testing.T) {
	t.Parallel()

	served := newHarness(t)
	served.write(manifest(fixtureAccent, fixtureInk))

	recorder := served.get(themePath, gmRequestor())

	for value := range strings.SplitSeq(recorder.Header().Get("Vary"), ",") {
		if strings.EqualFold(strings.TrimSpace(value), "sp_ui") {
			t.Errorf("Vary names sp_ui: %q. The document's independence from that "+
				"cookie is held by TestTheSheetIsByteIdenticalForEveryReaderAnd"+
				"Preference, so naming it here would be announcing a variation "+
				"there is none of", recorder.Header().Get("Vary"))
		}
	}

	if !strings.Contains(recorder.Header().Get("Vary"), "Cookie") {
		t.Errorf("Vary = %q, want Cookie: this route's *failure* responses are "+
			"reader-dependent, because the gate answers a member and a stranger "+
			"differently, and a stored response with no Vary matches any request",
			recorder.Header().Get("Vary"))
	}
}

// TestOneCampaignsThemeIsNotAnotherCampaigns is S-8.3 on this route.
//
// Two campaigns, one manifest each, and the assertion is that neither campaign can
// read the other's brand — through the body *and* through the validator, because a
// shared validator between two campaigns would let one campaign's cached answer
// stand in for the other's.
func TestOneCampaignsThemeIsNotAnotherCampaigns(t *testing.T) {
	t.Parallel()

	both := newHarness(t)
	both.write(manifest(fixtureAccent, fixtureInk))
	both.writeIn(otherSlug, manifest(otherAccent, fixtureInk))

	greyhaven := both.get(themePath, gmRequestor())
	saltmarsh := both.get("/c/"+otherSlug+"/"+sheetFile, gmRequestor())

	if greyhaven.Code != http.StatusOK || saltmarsh.Code != http.StatusOK {
		t.Fatalf("status = %d and %d, want two 200s", greyhaven.Code, saltmarsh.Code)
	}

	if !strings.Contains(greyhaven.Body.String(), fixtureAccent) {
		t.Errorf("the first campaign is not served its own brand:\n%s", greyhaven.Body)
	}

	if strings.Contains(saltmarsh.Body.String(), fixtureAccent) {
		t.Errorf("the other campaign's sheet carries this campaign's brand:\n%s",
			saltmarsh.Body)
	}

	if greyhaven.Header().Get("ETag") == saltmarsh.Header().Get("ETag") {
		t.Errorf("both campaigns advertise the validator %q; two different "+
			"representations must not share one (S-8.3)",
			greyhaven.Header().Get("ETag"))
	}

	// And the confinement: a manifest written into one root is not visible from
	// the other, so a campaign cannot brand another campaign by writing a file.
	onlyHere := newHarness(t)
	onlyHere.write(manifest(fixtureAccent, fixtureInk))

	recorder := onlyHere.get("/c/"+otherSlug+"/"+sheetFile, gmRequestor())
	if strings.Contains(recorder.Body.String(), fixtureAccent) {
		t.Errorf("the second campaign is served the first campaign's brand:\n%s",
			recorder.Body)
	}
}

// TestAConditionalRequestIsAnsweredWithNotModified: the `ETag` is the cache story,
// so it has to revalidate.
//
// A 304 with no body, and the validator identical to the 200's — which is the check
// that matters, because a 304 carrying a *different* validator teaches a client to
// keep asking.
func TestAConditionalRequestIsAnsweredWithNotModified(t *testing.T) {
	t.Parallel()

	served := newHarness(t)
	served.write(manifest(fixtureAccent, fixtureInk))

	first := served.get(themePath, gmRequestor())
	if first.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", first.Code)
	}

	second := served.getAs(themePath, gmRequestor(),
		http.Header{"If-None-Match": {first.Header().Get("ETag")}})

	if second.Code != http.StatusNotModified {
		t.Errorf("status = %d, want 304; body:\n%s", second.Code, second.Body)
	}

	if second.Body.Len() != 0 {
		t.Errorf("a 304 carries %d bytes of body, want none", second.Body.Len())
	}

	if got := second.Header().Get("ETag"); got != first.Header().Get("ETag") {
		t.Errorf("the 304 advertises %q and the 200 advertised %q",
			got, first.Header().Get("ETag"))
	}
}

// TestTheValidatorFollowsTheManifest: a GM who edits their brand gets new bytes, a
// new validator, and a 200 rather than a 304 against the old one.
//
// The 304 case is the one worth stating. A validator derived from anything but the
// generated bytes — a timestamp, the manifest's mtime, a counter — would answer 304
// here and leave the GM with a brand that never changes and a browser that never
// asks again.
func TestTheValidatorFollowsTheManifest(t *testing.T) {
	t.Parallel()

	served := newHarness(t)
	served.write(manifest(fixtureAccent, fixtureInk))

	first := served.get(themePath, gmRequestor())

	served.write(manifest(otherAccent, fixtureInk))

	second := served.getAs(themePath, gmRequestor(),
		http.Header{"If-None-Match": {first.Header().Get("ETag")}})

	if second.Code != http.StatusOK {
		t.Errorf("status = %d after the manifest changed, want 200: the validator "+
			"has to be a function of the generated bytes", second.Code)
	}

	if second.Header().Get("ETag") == first.Header().Get("ETag") {
		t.Error("the validator did not change after the manifest did")
	}

	if !strings.Contains(second.Body.String(), otherAccent) {
		t.Errorf("the new sheet does not carry the new brand:\n%s", second.Body)
	}
}

// TestARefusedManifestKeepsTheLastGoodTheme is §4.12.3's first requirement.
//
// The sequence is the whole claim: a valid manifest, then one that fails a floor,
// and the campaign must keep serving what it had. A route that answered the refusal
// with the core theme would be *safe* and still wrong — §4.12.3 asks for the last
// good theme precisely so that a GM editing one colour does not lose their theme
// mid-edit.
//
// The second half is the case the record does not spell out and the reason this test
// exists in two halves: a campaign that never had a good theme and then refuses one
// must serve the core theme, which is a *different* answer.
func TestARefusedManifestKeepsTheLastGoodTheme(t *testing.T) {
	t.Parallel()

	served := newHarness(t)
	served.write(manifest(fixtureAccent, fixtureInk))

	good := served.get(themePath, gmRequestor())
	if good.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", good.Code)
	}

	// A pair whose ink fails the text floor on its own accent: 3.73:1.
	served.write(manifest(fixtureAccent, "#0b1220"))

	after := served.get(themePath, gmRequestor())

	if after.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: a refused theme is not a broken route "+
			"(UI §4.12.3)", after.Code)
	}

	if after.Body.String() != good.Body.String() {
		t.Errorf("the refused manifest changed the sheet:\n--- before\n%s\n--- after\n%s",
			good.Body, after.Body)
	}

	if after.Header().Get("ETag") != good.Header().Get("ETag") {
		t.Error("the refused manifest changed the validator, so every browser " +
			"re-downloaded a sheet that did not change")
	}
}

// TestARefusedManifestWithNoGoodOneFallsBackToTheCoreTheme is the other half: the
// fallback is the core theme, and it is byte-identical to what a campaign with no
// manifest is served.
//
// Without this, "no manifest" and "a manifest that was refused" would be two
// different byte sequences for one visible state, and the validator would differ
// between a campaign that never tried and one that tried and failed — which is an
// existence oracle for the presence of a manifest, in a response whose whole purpose
// is to be cacheable.
func TestARefusedManifestWithNoGoodOneFallsBackToTheCoreTheme(t *testing.T) {
	t.Parallel()

	refused := newHarness(t)
	refused.write("tokens:\n  " + brandAccent + ": \"" + fixtureAccent + "\"\n")

	never := newHarness(t)

	one := refused.get(themePath, gmRequestor())
	two := never.get(themePath, gmRequestor())

	if one.Code != http.StatusOK || two.Code != http.StatusOK {
		t.Fatalf("status = %d and %d, want two 200s", one.Code, two.Code)
	}

	if one.Body.String() != two.Body.String() {
		t.Errorf("a refused manifest served different bytes from no manifest at all:\n"+
			"--- refused\n%s\n--- none\n%s", one.Body, two.Body)
	}

	if one.Header().Get("ETag") != two.Header().Get("ETag") {
		t.Error("a refused manifest and no manifest advertise different validators")
	}
}

// TestADeletedManifestWithdrawsTheTheme: removal is a decision, not a failure.
//
// A campaign that has been branded and then deletes `theme.yaml` must go back to the
// core theme. Keeping the remembered sheet would mean a brand a GM has removed
// follows them until the process restarts — and since the retention is process-local,
// "until the process restarts" is exactly the kind of invisible state this repository
// argues against.
func TestADeletedManifestWithdrawsTheTheme(t *testing.T) {
	t.Parallel()

	served := newHarness(t)
	served.write(manifest(fixtureAccent, fixtureInk))

	branded := served.get(themePath, gmRequestor())
	if !strings.Contains(branded.Body.String(), fixtureAccent) {
		t.Fatalf("the branded sheet does not carry the brand:\n%s", branded.Body)
	}

	served.remove()

	after := served.get(themePath, gmRequestor())

	if after.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", after.Code)
	}

	if strings.Contains(after.Body.String(), fixtureAccent) {
		t.Errorf("the theme survived the manifest being deleted:\n%s", after.Body)
	}

	if after.Body.String() != theme.CoreSheet() {
		t.Errorf("a withdrawn theme served %q, want the core sheet", after.Body)
	}

	if after.Header().Get("ETag") == branded.Header().Get("ETag") {
		t.Error("the validator did not change after the theme was withdrawn")
	}

	// And the withdrawal has to *clear* what was remembered, not merely stop
	// answering with it. A GM who deletes their manifest and then writes one that
	// fails a floor must get the core theme, not the brand they withdrew: a
	// retention map that kept the entry would resurrect the old theme through the
	// refusal path, which is the one branch nobody thinks about.
	served.write(manifest("#0f6f6a", fixtureInk))

	resurrected := served.get(themePath, gmRequestor())
	if strings.Contains(resurrected.Body.String(), fixtureAccent) {
		t.Errorf("the withdrawn theme came back through the refusal path:\n%s",
			resurrected.Body)
	}

	if resurrected.Body.String() != theme.CoreSheet() {
		t.Errorf("a withdrawn theme followed by a refused manifest served %q, want "+
			"the core sheet", resurrected.Body)
	}
}

// TestAnOversizedManifestIsRefusedNotApplied is the size cap on the *route*, and
// `TestTheManifestIsCappedAtItsOwnLimit` is the cap on the parser.
//
// They are two caps because they bound two different things, and only this test can
// see the first: `Parse` refuses an over-long byte slice whoever handed it, so a
// parser-only test passes whether or not the route ever stops reading one. A sync
// client that writes four gigabytes into `theme.yaml` reaches the read, not the
// parser, so the read is the cap that matters for it.
//
// The response is a 200 with the core theme, because an oversized file is a refused
// manifest and §4.12.3's direction is that a refused theme degrades to the product's
// own — not that the campaign stops loading.
func TestAnOversizedManifestIsRefusedNotApplied(t *testing.T) {
	t.Parallel()

	served := newHarness(t)

	// A valid manifest, padded past the cap with a YAML comment, so the *only*
	// thing wrong with it is its length.
	padding := strings.Repeat("p", theme.MaxManifestBytes)
	served.write(manifest(fixtureAccent, fixtureInk) + "# " + padding)

	recorder := served.get(themePath, gmRequestor())

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: an oversized manifest is a refused theme, "+
			"not a failed route (UI §4.12.3)", recorder.Code)
	}

	if recorder.Body.String() != theme.CoreSheet() {
		t.Errorf("an oversized manifest was applied:\n%s", recorder.Body)
	}

	if _, found := served.logs.find(invalidEventName); !found {
		t.Errorf("no %s line for the oversized manifest:\n%s",
			invalidEventName, served.logs.text())
	}
}

// TestARefusalIsLoggedAtErrorLevelWithTheCampaign: §4.12.3's second requirement,
// which exists because "a silently unreadable brand colour is a silent failure".
//
// The attributes are asserted and so is what is *absent*: no value from the manifest,
// because the file is attacker-reachable and a log aggregator is the one place a
// `[!secret]`-shaped problem becomes permanent. The log carries the rule that was
// broken, not the bytes that broke it.
func TestARefusalIsLoggedAtErrorLevelWithTheCampaign(t *testing.T) {
	t.Parallel()

	served := newHarness(t)
	// A pair that clears the text floor and both page floors except one: 2.97:1
	// against the dark page, which is what makes the refusal name a theme.
	served.write(manifest("#0f6f6a", fixtureInk))

	served.get(themePath, gmRequestor())

	line, found := served.logs.find(invalidEventName)
	if !found {
		t.Fatalf("no %s line was logged; a refused brand is otherwise a silent "+
			"failure (UI §4.12.3)\nlogged: %s", invalidEventName, served.logs.text())
	}

	if line.level != slog.LevelError {
		t.Errorf("level = %s, want ERROR: the record says error level", line.level)
	}

	if got := line.attributes["campaign_id"]; got != "1" {
		t.Errorf("campaign_id = %q, want %q; the line has to name the campaign a GM "+
			"has to fix", got, "1")
	}

	if got := line.attributes["reason"]; !strings.Contains(got, "dark") {
		t.Errorf("reason = %q, and it does not say which page the brand failed on",
			got)
	}

	// The reason names the *rule*; the token does not travel in it. That is what
	// makes `refusalReason` reading `RefusalError.Reason` rather than
	// `err.Error()` — and a log line that echoed the manifest's own bytes would be
	// S-12.3's failure, so the absence is asserted rather than assumed.
	if got := line.attributes["reason"]; strings.Contains(got, brandAccent) {
		t.Errorf("reason = %q carries the manifest's token name; the line should name "+
			"the rule that was broken, with nothing taken from the campaign's file", got)
	}

	for name, value := range line.attributes {
		for _, forbidden := range []string{"#0f6f6a", fixtureAccent, fixtureInk} {
			if strings.Contains(value, forbidden) {
				t.Errorf("the %s attribute carries %q, which is a colour from the "+
					"campaign's file: S-12.3 forbids putting campaign content in a "+
					"log line, and a manifest is untrusted input (Obsidian sync)",
					name, forbidden)
			}
		}
	}
}

// invalidEventName is §4.12.3's event name, as this route logs it.
//
// Spelled here rather than read from the package because the name is not yet in
// `observability.AllEventNames()`; see the package clause in theme.go. When the
// integrator adds the constant, this becomes `string(observability.EventThemeBrandInvalid)`
// and the assertion below is the evidence the two agree.
const invalidEventName = "theme.brand_invalid"

// TestTheRouteIsBehindTheReadGate: a private campaign's theme is a 404 to a
// stranger, for the same reason its pages are.
//
// The gate is `mountCampaignRoutes`' mount, not a check in the handler — ADR 0024 —
// and this is the test for that claim on this route. A handler that asked who was
// asking would be a second copy of the S-8 matrix, and the copy nobody reviews is the
// one that eventually answers 403 where the gate answered 404, which turns a private
// campaign's existence into a fact a stranger can learn one status code at a time.
//
// Four readers, because the matrix has four rows and a test with three cannot make
// a claim about four. The two who pass are a GM and a **player**: `public` grants
// wiki read to non-members, but a *private* campaign's members of any role may read
// it, so the player is the reader a wrong gate most easily loses.
func TestTheRouteIsBehindTheReadGate(t *testing.T) {
	t.Parallel()

	private := newHarness(t).private()
	private.write(manifest(fixtureAccent, fixtureInk))

	for name, testCase := range map[string]struct {
		requestor domain.Requestor
		want      int
	}{
		"anonymous":               {anonymousRequestor(), http.StatusNotFound},
		"signed in, not a member": {outsiderRequestor(), http.StatusNotFound},
		"player":                  {playerRequestor(), http.StatusOK},
		"gm":                      {gmRequestor(), http.StatusOK},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			recorder := private.get(themePath, testCase.requestor)
			if recorder.Code != testCase.want {
				t.Errorf("status = %d, want %d", recorder.Code, testCase.want)
			}
		})
	}
}

// TestTheRouteIsNotAFileServer: the URL space is the slug and nothing else.
//
// §4.12.2's claim is that a campaign cannot ship CSS, and this is the half of it
// that is about the URL rather than about the manifest: there is no path under
// `/c/{slug}/theme.css/`, and a campaign's own stylesheet is served by the assets
// route as a static file that the shell never links. A theme route with a `{path...}`
// wildcard would be a route whose reachable set is a function of a campaign's
// directory listing.
func TestTheRouteIsNotAFileServer(t *testing.T) {
	t.Parallel()

	served := newHarness(t)
	served.write(manifest(fixtureAccent, fixtureInk))

	for _, target := range []string{
		themePath + "/",
		themePath + "/extra.css",
		"/c/" + testSlug + "/theme.yaml",
		"/c/" + testSlug + "/theme",
	} {
		recorder := served.get(target, gmRequestor())

		if recorder.Code != http.StatusNotFound {
			t.Errorf("GET %s answered %d, want 404; this route reads one fixed "+
				"filename and its only input is the slug", target, recorder.Code)
		}
	}
}

// TestACampaignWithoutARootIsAFaultNotAnAbsentTheme: the 500.
//
// A campaign with no retained content root is a degraded instance (S-4.5). The route
// must not answer "no theme" for it, because the campaign may well have a perfectly
// good manifest on disk — and "your campaign looks unbranded" is a thing this route
// must never tell a reader on the strength of its own failure.
func TestACampaignWithoutARootIsAFaultNotAnAbsentTheme(t *testing.T) {
	t.Parallel()

	served := newHarness(t)

	recorder := served.get("/c/nosuchcampaign/"+theme.ManifestFileName, gmRequestor())
	if recorder.Code != http.StatusNotFound {
		t.Errorf("status = %d for a campaign the store does not know, want 404: the "+
			"gate answers that, and this handler is never reached", recorder.Code)
	}

	// Registered with the gate, but with no root in the registry: the shape of a
	// degraded instance, and the only way to reach the 500.
	noRoot := &theme.Handler{Roots: emptyRoots{}, Logger: served.logs.logger()}

	recorder = serveWithoutRoot(t, noRoot)
	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("status = %d with no content root, want 500; reporting \"no theme\" "+
			"for our own failure would be a lie served to every reader", recorder.Code)
	}

	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q on a failure, want no-store: there is no "+
			"validator for a failure and a pinned one outlives the fault", got)
	}

	if !strings.HasPrefix(recorder.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("Content-Type = %q on a failure, want plain text: a stylesheet is a "+
			"subresource, and one returning an HTML document is a parse error nobody "+
			"sees", recorder.Header().Get("Content-Type"))
	}
}

// emptyRoots is a registry with no roots in it, which is the state a degraded
// instance is in.
type emptyRoots struct{}

func (emptyRoots) Get(string) (*content.Root, error) {
	return nil, content.ErrNoRoot
}

// serveWithoutRoot mounts a handler over a campaign the store knows and no root.
func serveWithoutRoot(t *testing.T, handler *theme.Handler) *httptest.ResponseRecorder {
	t.Helper()

	rows := campaignStore{
		campaigns: map[string]domain.Campaign{
			testSlug: {
				ID: testCampID, Slug: testSlug, Visibility: domain.VisibilityPublic,
			},
		},
		members: map[int64]domain.Role{gmUser: domain.RoleGM},
	}

	campaignMux := http.NewServeMux()
	theme.Mount(campaignMux, handler)

	outer := http.NewServeMux()
	outer.Handle("/c/{slug}/",
		campaigns.Resolve(rows)(middleware.Chain(campaignMux, campaigns.RequireRead)),
	)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, themePath, http.NoBody)
	req = req.WithContext(identity.WithRequestor(req.Context(), gmRequestor()))

	recorder := httptest.NewRecorder()
	middleware.RequestID(outer).ServeHTTP(recorder, req)

	return recorder
}

// TestTheManifestIsReadThroughTheConfinement is S-3.5 on this route, and the
// campaign's root is the only thing the path goes through.
//
// A symlinked `theme.yaml` is the fixture, because it is the case `os.Root` and a
// `filepath.Clean`-plus-prefix-check disagree about: the second answers "inside the
// root" for a link whose *target* is outside it. The route's policy is
// `content.RefuseSymlinks`, so the file is invisible and the campaign reads as
// unbranded — a refusal, not an error, and not a read of somebody else's file.
func TestTheManifestIsReadThroughTheConfinement(t *testing.T) {
	t.Parallel()

	served := newHarness(t)

	outside := filepath.Join(t.TempDir(), "outside.yaml")
	if err := os.WriteFile(outside, []byte(manifest(otherAccent, fixtureInk)), 0o600); err != nil {
		t.Fatalf("write the outside manifest: %v", err)
	}

	link := filepath.Join(served.dirs[testSlug], theme.ManifestFileName)
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks are unavailable here: %v", err)
	}

	recorder := served.get(themePath, gmRequestor())

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: a refused manifest is not a failed route",
			recorder.Code)
	}

	if strings.Contains(recorder.Body.String(), otherAccent) {
		t.Errorf("the route read a manifest through a symlink out of the campaign's "+
			"root:\n%s", recorder.Body)
	}
}

// TestTheManifestReadIsBoundedAndNotTheFilesSize is the one cap in this package
// that a behavioural test cannot otherwise reach, so it gets one.
//
// `io.LimitReader` in `readManifest` bounds how many bytes are **read**, and the
// difference between that and refusing the bytes afterwards is the difference between
// 16 KiB of allocation and the whole file. §4.12.2 calls the manifest attacker-reachable
// input, and an Obsidian sync client can put anything in the file; a route that
// reads it whole turns one request into one gigabyte of allocation.
//
// The fixture is a **sparse** file — `os.Truncate` extends it without writing a byte
// — so the test costs nothing on disk. The measurement is `runtime.MemStats`, the
// same instrument `content.frontmatter_test.go`'s alias bomb uses, and the ceiling is
// eight megabytes: a hundred times what the bounded read allocates, and a thousand
// times smaller than the file.
//
// **Mutation:** replacing `io.LimitReader(file, MaxManifestBytes+1)` with `file`
// fails this test.
//
// No `t.Parallel()`, and that is the point rather than an omission: the
// measurement is process-wide, so a sibling test allocating its own fixtures while
// this one reads its numbers would make a bounded read look unbounded. Go runs a
// package's sequential tests one at a time and its parallel ones together
// afterwards, so dropping the call is what buys an uncontended measurement.
func TestTheManifestReadIsBoundedAndNotTheFilesSize(t *testing.T) {
	const (
		fileSize = int64(1) << 30 // 1 GiB, sparse
		ceiling  = 8 << 20        // 8 MiB allocated for a request
	)

	served := newHarness(t)

	// The bytes are a valid manifest, so the refusal that follows is the size cap
	// and not a parse error — and the padding is sparse, so nothing is written.
	path := filepath.Join(served.dirs[testSlug], theme.ManifestFileName)
	if err := os.WriteFile(
		path,
		[]byte(manifest(fixtureAccent, fixtureInk)+"# "),
		0o600,
	); err != nil {
		t.Fatalf("write the manifest: %v", err)
	}

	if err := os.Truncate(path, fileSize); err != nil {
		t.Skipf("this filesystem cannot make a sparse file: %v", err)
	}

	runtime.GC()

	var before, after runtime.MemStats

	runtime.ReadMemStats(&before)

	if recorder := served.get(themePath, gmRequestor()); recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	runtime.ReadMemStats(&after)

	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > ceiling {
		t.Errorf("one request allocated %d bytes for a %d-byte manifest; the read "+
			"is bounded by io.LimitReader and nothing else, so a campaign's file "+
			"size is a request's memory cost without it",
			allocated, fileSize)
	}
}

// recordingLogger captures what a route logged.
//
// A handler with a mutex and a slice rather than a test framework's log capture,
// because the assertion is about *levels and attributes* and a string of formatted
// output would make every one of them a substring guess.
//
// It is the handler itself, rather than a wrapper around one: `slog.New(r)` puts
// `r.Handle` on the chain, so a test reads the record's level and attributes
// directly rather than parsing a formatted line. The assertion is about levels and
// attribute *values*, and a substring search over formatted output would make every
// one of them a guess about how `TextHandler` quotes a string.
type recordingLogger struct {
	mu    sync.Mutex
	lines []loggedLine
}

func newRecordingLogger() *recordingLogger {
	return &recordingLogger{}
}

// loggedLine is one captured line.
type loggedLine struct {
	message    string
	level      slog.Level
	attributes map[string]string
}

// logger builds the slog.Logger the handler is given.
func (r *recordingLogger) logger() *slog.Logger {
	return slog.New(r)
}

// find returns the first line logged under message.
func (r *recordingLogger) find(message string) (loggedLine, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, line := range r.lines {
		if line.message == message {
			return line, true
		}
	}

	return loggedLine{}, false
}

// text is every line, for a failure message that has to show what *was* logged.
func (r *recordingLogger) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var out strings.Builder

	for _, line := range r.lines {
		out.WriteString(line.message)
		out.WriteString(" ")
		out.WriteString(line.level.String())
		out.WriteString("\n")
	}

	return out.String()
}

// Enabled implements slog.Handler.
func (r *recordingLogger) Enabled(_ context.Context, _ slog.Level) bool { return true }

// Handle implements slog.Handler.
func (r *recordingLogger) Handle(_ context.Context, record slog.Record) error {
	line := loggedLine{
		message:    record.Message,
		level:      record.Level,
		attributes: map[string]string{},
	}

	record.Attrs(func(attr slog.Attr) bool {
		line.attributes[attr.Key] = attr.Value.String()

		return true
	})

	r.mu.Lock()
	defer r.mu.Unlock()

	r.lines = append(r.lines, line)

	return nil
}

// WithAttrs implements slog.Handler. The route logs no groups and builds no child
// loggers, so the attributes are dropped rather than carried; the tests assert on
// flat attributes.
func (r *recordingLogger) WithAttrs([]slog.Attr) slog.Handler { return r }

// WithGroup implements slog.Handler.
func (r *recordingLogger) WithGroup(string) slog.Handler { return r }
