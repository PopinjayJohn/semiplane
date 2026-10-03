package play_test

// The document route's own behaviour: the URL scheme S-9 fixes, the S-8 matrix
// over `GET /play`, the headers, the title, and the three things a reader's role
// decides about their bytes.
//
// # Why these are separate from the socket's tests
//
// `play_test.go` holds the transport's claims — a handshake, a refusal, a peer
// slot. This file holds the *document's*: that `/play` answers a browser with
// HTML rather than a handshake failure, that the gate's refusals over this route
// are byte-identical to each other, and that what a player may see is decided
// before the bytes are written rather than by a stylesheet after. Each claim is
// observed on the response of a real mounted route, because a document assembled
// in a unit test is a document assembled by the test.
//
// # What is deliberately not asserted here
//
// Structure, landmarks, target sizes and the vocabulary rule live in
// `route_a11y_test.go`, which asks a different question of the same bytes — and
// the map's *populated* branch lives in the component tests, because no fixture
// in this package serves a scene (there is no scene source to serve yet; see the
// integration report's open gaps).

import (
	"context"
	"errors"
	"html"
	"net/http"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/domain/systems/dnd5e"
	"github.com/semiplane/semiplane/internal/httpapi/play"
	"github.com/semiplane/semiplane/internal/web/components"
	webplay "github.com/semiplane/semiplane/internal/web/components/play"
)

// TestTheURLSchemeSplitsTheDocumentFromTheSocket is S-9's row of the URL scheme:
// `/c/{slug}/play` is the VTT document, `/c/{slug}/ws` is the WebSocket.
//
// The two halves are asserted together because the whole point of the split is
// that **both answers exist at once**: before this work item `/play` *was* the
// upgrade, so a player who opened the address in a browser received a handshake
// failure where the record promises a table. A test of `/play` alone would pass
// on a build that had moved the document but broken the socket, and one of
// `/ws` alone on the reverse.
//
// The refusal rows are part of the claim rather than the socket matrix's
// business repeated here: a plain GET reaches the *gate* before anything else,
// and "the gate is on both routes" is only true if both routes answer it.
func TestTheURLSchemeSplitsTheDocumentFromTheSocket(t *testing.T) {
	t.Parallel()

	harness := newHarness(t, &stubResolver{})

	document := harness.get(gmSlug, gmUserID)
	if document.status != http.StatusOK {
		t.Fatalf("GET /c/%s/play as its GM = %d, want %d: S-9's VTT address must "+
			"answer a browser with the table, not with a handshake failure",
			gmSlug, document.status, http.StatusOK)
	}

	if got := document.header.Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("GET /play Content-Type = %q, want %q", got, "text/html; charset=utf-8")
	}

	// Measured rather than assumed in both directions: the serializer emits the
	// doctype in lower case, and the *claim* is that this is a document, not what
	// case its prologue is in.
	if body := strings.ToLower(
		strings.TrimSpace(document.body),
	); !strings.HasPrefix(
		body,
		"<!doctype html>",
	) {
		t.Errorf("GET /play body begins %q, want a document: the record's VTT row is "+
			"an HTML page a browser can be pointed at", truncate(document.body))
	}

	// The other half: a plain GET at the socket's address is neither a document
	// nor a handshake. `websocket.Accept` answers **426** — "handshake request
	// must contain Connection: Upgrade" — because a browser-shaped GET carries no
	// upgrade headers at all, and that is the correct answer for a *socket*
	// route. What is being asserted is that it is not the answer `/play` gives,
	// and that the number is the library's rather than a route refusal: the gate
	// already admitted this reader, so a 403 here would be the handler claiming
	// something about access it had already established.
	handshake := harness.fetch("/c/"+gmSlug+"/ws", gmUserID)
	if handshake.status != http.StatusUpgradeRequired {
		t.Errorf("GET /c/%s/ws without an upgrade = %d, want %d",
			gmSlug, handshake.status, http.StatusUpgradeRequired)
	}

	if strings.Contains(handshake.body, "<!DOCTYPE") {
		t.Errorf("GET /ws answered with a document; the socket route must not render " +
			"HTML, or the two addresses answer the same question twice")
	}

	if got := handshake.header.Get("Content-Type"); strings.HasPrefix(got, "text/html") {
		t.Errorf("GET /ws Content-Type = %q, want something other than the document's",
			got)
	}

	// The gate is on the socket route too, and it answers before the handshake
	// machinery is anywhere near the request.
	if got := harness.fetch("/c/"+gmSlug+"/ws", 0).status; got != http.StatusUnauthorized {
		t.Errorf("GET /ws as an anonymous reader = %d, want %d: S-8 holds for both "+
			"routes of the pair, not only the one a browser can render",
			got, http.StatusUnauthorized)
	}

	// And a GET that failed its handshake must not have kept a peer slot. The
	// join is deliberately *before* the upgrade (the refusal is a status code),
	// so the release is the deferred leave — which is exactly the thing worth
	// watching, since a leaked slot is a table that fills up by being browsed.
	waitFor(t, "the failed handshake's peer slot to be released", func() bool {
		return harness.hub.Stats().Peers == 0
	})
}

// TestTheDocumentAnswersTheS8Matrix is the socket matrix's sibling, over
// `GET /play`: the same seven rows, because the gate is one `Mount` wrapping two
// routes and a matrix that checked only one of them would pass a build whose
// document route had been mounted bare.
//
// The refused rows also carry the security invariant that reaches them: every
// gate response is `private, no-store`. These four answers are reader-dependent
// by construction — the same URL is a 404 for one reader and a 200 for another —
// so a cache that stored one would serve it to the other.
func TestTheDocumentAnswersTheS8Matrix(t *testing.T) {
	t.Parallel()

	const anonymous = int64(0)

	cases := []struct {
		name   string
		slug   string
		user   int64
		status int
	}{
		{
			name:   "the GM reaches the table",
			slug:   gmSlug,
			user:   gmUserID,
			status: http.StatusOK,
		},
		{
			name:   "a player reaches the table",
			slug:   gmSlug,
			user:   playerUserID,
			status: http.StatusOK,
		},
		{
			name:   "an anonymous reader of a public campaign is refused",
			slug:   gmSlug,
			user:   anonymous,
			status: http.StatusUnauthorized,
		},
		{
			name:   "an authenticated non-member of a public campaign is refused",
			slug:   gmSlug,
			user:   outsiderUserID,
			status: http.StatusForbidden,
		},
		{
			name:   "an anonymous reader of a private campaign is a 404",
			slug:   privateSlug,
			user:   anonymous,
			status: http.StatusNotFound,
		},
		{
			name:   "an authenticated non-member of a private campaign is a 404",
			slug:   privateSlug,
			user:   outsiderUserID,
			status: http.StatusNotFound,
		},
		{
			name:   "a slug that resolves to nothing is the same 404",
			slug:   absentSlug,
			user:   outsiderUserID,
			status: http.StatusNotFound,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			harness := newHarness(t, &stubResolver{})
			got := harness.get(testCase.slug, testCase.user)

			if got.status != testCase.status {
				t.Errorf("GET /c/%s/play as user %d = %d, want %d",
					testCase.slug, testCase.user, got.status, testCase.status)
			}

			if testCase.status == http.StatusOK {
				return
			}

			if cache := got.header.Get("Cache-Control"); cache != "private, no-store" {
				t.Errorf("GET /play as user %d answered %d with Cache-Control %q, want "+
					"%q: the refusal depends on who asked, so a shared cache that stored "+
					"it would answer the next reader with this one's verdict",
					testCase.user, got.status, cache, "private, no-store")
			}
		})
	}
}

// TestAPrivateCampaignAndAnAbsentOneAreIndistinguishable is the 404 half of S-8
// asserted on the body and not only on the status.
//
// The status alone is the weak form: two refusals that both say 404 but carry
// different text still tell an outsider which campaigns exist — "not a member"
// and "not here" are the same fact wearing two coats. The pair is an
// *authenticated* reader on a private campaign versus a slug that resolves to
// nothing, because that is the pair where a handler that worded its refusals
// differently would be caught.
func TestAPrivateCampaignAndAnAbsentOneAreIndistinguishable(t *testing.T) {
	t.Parallel()

	harness := newHarness(t, &stubResolver{})

	private := harness.get(privateSlug, outsiderUserID)
	absent := harness.get(absentSlug, outsiderUserID)

	if private.status != http.StatusNotFound || absent.status != http.StatusNotFound {
		t.Fatalf("statuses = %d and %d, want %d and %d; the body comparison below "+
			"assumes the statuses already agree",
			private.status, absent.status, http.StatusNotFound, http.StatusNotFound)
	}

	if private.body != absent.body {
		t.Errorf("the private campaign's 404 body is %q and the absent one's is %q; "+
			"they must be byte-identical, or the difference is an existence oracle",
			private.body, absent.body)
	}
}

// TestTheDocumentIsPrivateAndReaderDependent is the header half of ADR 0035's
// corrected claim, on the route that inherits it.
//
// The wiki route carries `Vary: Cookie` because its shell reads the reader; this
// document does the same and for a sharper reason — the token list's rows are
// *filtered by role*, so two readers of one URL receive different bytes rather
// than different decoration. `private, no-store` is the other half: no shared
// cache may hold either copy.
//
// The bodies-differ assertion is the one that makes the headers meaningful. A
// test that only found `Vary: Cookie` in the markup would pass on a document
// that had stopped varying at all.
func TestTheDocumentIsPrivateAndReaderDependent(t *testing.T) {
	t.Parallel()

	harness := newHarness(t, &stubResolver{})

	gm := harness.get(gmSlug, gmUserID)
	player := harness.get(gmSlug, playerUserID)

	for _, reader := range []struct {
		who string
		doc documentResponse
	}{
		{who: "the GM", doc: gm},
		{who: "a player", doc: player},
	} {
		if reader.doc.status != http.StatusOK {
			t.Fatalf("GET /play as %s = %d, want %d", reader.who, reader.doc.status,
				http.StatusOK)
		}

		if got := reader.doc.header.Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Errorf("GET /play as %s has Content-Type %q, want %q",
				reader.who, got, "text/html; charset=utf-8")
		}

		if got := reader.doc.header.Get("Cache-Control"); got != "private, no-store" {
			t.Errorf("GET /play as %s has Cache-Control %q, want %q",
				reader.who, got, "private, no-store")
		}

		if got := reader.doc.header.Get("Vary"); got != "Cookie" {
			t.Errorf("GET /play as %s has Vary %q, want %q: the same URL answers two "+
				"readers differently, which is the fact the header states",
				reader.who, got, "Cookie")
		}
	}

	if gm.body == player.body {
		t.Error("the GM's document and the player's are byte-identical; then nothing " +
			"varies and `Vary: Cookie` is a header asserting a difference that is not " +
			"there — the token list's visibility filter would have stopped working and " +
			"every claim below it with it")
	}
}

// TestTheTitleIsThreePartsInTheRecordsForm is UI §7.2's `<title>` shape —
// "page — campaign — instance" — and §8.3's rank-1 claim about the heading.
//
// Three subtests because three things fall back: the instance to the product's
// name when it is unconfigured, the campaign to its slug when it was registered
// without a name, and neither fallback is this route's invention — both are the
// shell's own, spelled again in `document.go` because they are unexported there.
// A title of "Table — — " tells a reader nothing, and a heading of "Table" tells
// them the route rather than where they are.
func TestTheTitleIsThreePartsInTheRecordsForm(t *testing.T) {
	t.Parallel()

	t.Run("the campaign and the instance, in the record's order", func(t *testing.T) {
		t.Parallel()

		got := newHarness(t, &stubResolver{}).get(gmSlug, gmUserID)

		want := "<title>Table — The Gilded Cage — Greyhaven</title>"
		if !strings.Contains(got.body, want) {
			t.Errorf("the document's title is not %q; §7.2's form is page, campaign, "+
				"instance, in that order", want)
		}

		if !strings.Contains(got.body, ">The Gilded Cage</h1>") {
			t.Error("the <h1> is not the campaign's name; §8.3's rank 1 is the current " +
				"location, and the campaign is what this route is inside")
		}
	})

	t.Run("an unconfigured instance falls back to the product", func(t *testing.T) {
		t.Parallel()

		harness := newMounted(t, mount{handler: func(handler *play.Handler) {
			handler.Instance = components.InstanceView{}
		}})

		want := "<title>Table — The Gilded Cage — semiplane</title>"
		if got := harness.get(gmSlug, gmUserID); !strings.Contains(got.body, want) {
			t.Errorf("the document's title is not %q; an unconfigured instance still "+
				"has a first page, and the fallback is the product's name", want)
		}
	})

	t.Run("a campaign registered without a name falls back to its slug", func(t *testing.T) {
		t.Parallel()

		harness := newHarness(t, &stubResolver{})
		addCampaign(t, harness, domain.Campaign{
			ID:         3,
			Slug:       "quiet-harbour",
			Visibility: domain.VisibilityPublic,
			SystemID:   string(dnd5e.SystemID),
		}, map[int64]domain.Membership{
			gmUserID: {CampaignID: 3, UserID: gmUserID, Role: domain.RoleGM},
		})

		got := harness.get("quiet-harbour", gmUserID)

		want := "<title>Table — quiet-harbour — Greyhaven</title>"
		if !strings.Contains(got.body, want) {
			t.Errorf("the document's title is not %q; a campaign with no name still "+
				"has a URL, and the title falls back to it", want)
		}

		if !strings.Contains(got.body, ">quiet-harbour</h1>") {
			t.Errorf("the <h1> is not %q; the heading falls back the same way the "+
				"title's middle part does, or two halves of one document disagree",
				"quiet-harbour")
		}
	})
}

// TestTheTokenListShowsOnlyWhatTheReadersRoleEntitlesThemTo is UI §4.5's rule
// one level down: what a player must not see is **absent from their bytes**.
//
// The fixture carries two placements and the difference between them is the
// whole claim — one visible, one not — so the two rows are the same snapshot
// read by two roles. The negative assertion searches for the id itself rather
// than for `data-placement="p-hidden"`, because a hidden id that reappeared in
// any other attribute would still be the leak.
func TestTheTokenListShowsOnlyWhatTheReadersRoleEntitlesThemTo(t *testing.T) {
	t.Parallel()

	harness := newHarness(t, &stubResolver{})

	gm := harness.get(gmSlug, gmUserID)
	player := harness.get(gmSlug, playerUserID)

	for _, id := range []string{"p-shown", "p-hidden"} {
		if !strings.Contains(gm.body, `data-placement="`+id+`"`) {
			t.Errorf("the GM's document has no placement %q; a GM sees every "+
				"placement, and a fixture whose GM sees nothing would make every "+
				"assertion below vacuous", id)
		}
	}

	if !strings.Contains(player.body, `data-placement="p-shown"`) {
		t.Error("a player's document has no visible placement; §4.5 hides what the " +
			"role may not see, not everything")
	}

	if strings.Contains(player.body, "p-hidden") {
		t.Error("a player's document carries the hidden placement somewhere in its " +
			"bytes; a `display:none` row is a row the reader's own browser holds, " +
			"which is the failure §4.5's \"absent, not hidden\" exists to prevent")
	}
}

// TestTheDieSheetRendersTheSystemsNotationOrTheHonestEmptyState is S-10.6's
// first row reached from the UI side: no resolvable system, no notation, and
// **no 500** either.
//
// Three states, each a real one rather than a fault: the engine answering (the
// default fixture), nothing wired (a composition root mid-assembly), and a
// system that fails to answer (a pack that did not load). Each of the last two
// renders §4.7's designed empty state — the sentence `RollUnavailable` spells —
// instead of a heading over an empty list, and the document still answers 200,
// because four landmarks that already rendered are worth more than an error page.
func TestTheDieSheetRendersTheSystemsNotationOrTheHonestEmptyState(t *testing.T) {
	t.Parallel()

	t.Run("the system's own notation", func(t *testing.T) {
		t.Parallel()

		got := newHarness(t, &stubResolver{}).get(gmSlug, gmUserID)

		if !strings.Contains(got.body, `data-testid="play-roll-terms"`) {
			t.Error("the document has no roll-terms list; the fixture's campaign has " +
				"the 5e engine wired, so the die sheet has a notation to render")
		}

		if !strings.Contains(got.body, `class="roll-term"`) {
			t.Error("the roll-terms list has no term in it; a heading over an empty " +
				"list is the state §4.7's empty branch exists to avoid, reached from " +
				"the populated side")
		}

		if strings.Contains(got.body, `data-testid="play-roll-empty"`) {
			t.Error("the document rendered the empty state although the campaign's " +
				"system answers")
		}
	})

	t.Run("no system wired", func(t *testing.T) {
		t.Parallel()

		harness := newMounted(t, mount{handler: func(handler *play.Handler) {
			handler.Systems = nil
		}})

		assertTheHonestEmptyState(t, harness.get(gmSlug, gmUserID))
	})

	t.Run("a system that fails to answer", func(t *testing.T) {
		t.Parallel()

		harness := newMounted(t, mount{handler: func(handler *play.Handler) {
			handler.Systems = func(context.Context, int64) (rules.System, error) {
				return nil, errors.New("the pack did not load")
			}
		}})

		assertTheHonestEmptyState(t, harness.get(gmSlug, gmUserID))
	})
}

// assertTheHonestEmptyState is the shared half of the die sheet's two empty
// states: a 200, the sentence, the empty branch present and the terms absent.
//
// One function because the two states are one claim — "no notation, honestly" —
// and two copies of the four assertions would be two answers to whether the
// fallback rendered.
func assertTheHonestEmptyState(t *testing.T, got documentResponse) {
	t.Helper()

	if got.status != http.StatusOK {
		t.Fatalf("GET /play = %d, want %d: a missing grammar is a state, not a "+
			"fault, and the four landmarks already rendered",
			got.status, http.StatusOK)
	}

	// The sentence as the *bytes* carry it: templ runs every expression through
	// `html.EscapeString`, so the apostrophe in "campaign's" arrives as `&#39;`
	// and a test that compared the raw constant would fail on a document that
	// renders it correctly. The marker below is the unescaped hook, so between
	// them the branch and its copy are both held.
	want := html.EscapeString(webplay.RollUnavailable)

	if !strings.Contains(got.body, want) {
		t.Errorf("the document does not carry %q; §4.7's empty state is the sentence "+
			"itself, not a shrug", webplay.RollUnavailable)
	}

	if !strings.Contains(got.body, `data-testid="play-roll-empty"`) {
		t.Error("the document has no empty-state marker for the die sheet")
	}

	if strings.Contains(got.body, `data-testid="play-roll-terms"`) {
		t.Error("the document rendered a roll-terms list beside the empty state; two " +
			"answers to whether this campaign has a notation")
	}
}

// TestTheServedDocumentHasNoMapSurfaceWithoutAScene is the scene branch's absent
// half over real bytes.
//
// `static/js/map/table.js` walks `[data-map-surface]` on load and refuses a
// surface with no `data-map-src`, so an absent surface is not a stylistic
// preference: a rendered surface with no scene puts a load error on every table
// of every campaign that has not chosen an image yet. The populated branch is
// the component tests' — no fixture here serves a scene (there is no scene
// source yet), so asserting presence would be asserting a fixture.
func TestTheServedDocumentHasNoMapSurfaceWithoutAScene(t *testing.T) {
	t.Parallel()

	got := newHarness(t, &stubResolver{}).get(gmSlug, gmUserID)

	if strings.Contains(got.body, "data-map-surface") {
		t.Error("the document renders data-map-surface with no scene behind it; " +
			"table.js refuses such a surface, so every load of this table would fail")
	}

	if !strings.Contains(got.body, `data-testid="play-map-empty"`) {
		t.Error("the document has no scene and no empty state; §4.7's designed " +
			"absence is a rendered sentence, not a hole in the centre")
	}
}

// TestTheNavigationOffersEachDestinationToTheRoleThatCanUseIt is UI §4.3's
// "absent, not disabled" over this route's four destinations, per role.
//
// The assertions pair each address with its hook in one substring rather than
// checking for both separately, because `href` and `data-testid` sit on the same
// element: two independent `Contains` calls would pass on a document where the
// right address hung off the wrong row.
//
// The no-system campaign is `tableHref`'s second condition made reachable — a
// member of a campaign this build cannot play still gets a document, and the
// navigation omits the Table destination rather than offering one that 404s.
func TestTheNavigationOffersEachDestinationToTheRoleThatCanUseIt(t *testing.T) {
	t.Parallel()

	t.Run("a GM's four destinations", func(t *testing.T) {
		t.Parallel()

		got := newHarness(t, &stubResolver{}).get(gmSlug, gmUserID)

		for _, link := range []struct{ address, hook string }{
			{address: "/c/gilded-cage/play", hook: "nav-table"},
			{address: "/c/gilded-cage/search", hook: "nav-search"},
			{address: "/c/gilded-cage/settings", hook: "nav-admin"},
			{address: "/c/gilded-cage", hook: "footer-leave"},
		} {
			pair := `href="` + link.address + `" data-testid="` + link.hook + `"`
			if !strings.Contains(got.body, pair) {
				t.Errorf("the GM's document has no %q; §4.3 lists Table, Search and "+
					"Admin in that order, and the footer's row is where \"Leave table\" "+
					"lives (UI §4.2)", pair)
			}
		}
	})

	t.Run("a player has no Admin at all", func(t *testing.T) {
		t.Parallel()

		got := newHarness(t, &stubResolver{}).get(gmSlug, playerUserID)

		if !strings.Contains(got.body, `href="/c/gilded-cage/play" data-testid="nav-table"`) {
			t.Error("a player has no Table destination; a player's tier reaches the " +
				"table, and the nav's condition is the tier")
		}

		if strings.Contains(got.body, "nav-admin") {
			t.Error("a player's document carries an Admin destination somewhere; §4.3 " +
				"requires edit affordances to be absent, not disabled, and a disabled " +
				"control is a control that says a settings page exists")
		}
	})

	t.Run("a campaign with no gameplay system has no Table destination",
		func(t *testing.T) {
			t.Parallel()

			harness := newHarness(t, &stubResolver{})
			addCampaign(t, harness, domain.Campaign{
				ID:         4,
				Slug:       "salt-road",
				Name:       "The Salt Road",
				Visibility: domain.VisibilityPrivate,
				// No SystemID: `tableHref` mirrors the wiki route's condition, and
				// a member of a campaign this build cannot play has no table to
				// offer — the link would answer 404 on the one page it appears on.
			}, map[int64]domain.Membership{
				gmUserID: {CampaignID: 4, UserID: gmUserID, Role: domain.RoleGM},
			})

			got := harness.get("salt-road", gmUserID)
			if got.status != http.StatusOK {
				t.Fatalf("GET /play on a campaign with no system = %d, want %d: the "+
					"document does not depend on a gameplay system, only one link does",
					got.status, http.StatusOK)
			}

			if strings.Contains(got.body, "nav-table") {
				t.Error("the navigation offers a Table destination on a campaign with " +
					"no gameplay system; the destination would 404")
			}

			if !strings.Contains(got.body, `href="/c/salt-road/settings" data-testid="nav-admin"`) {
				t.Error("the GM of a system-less campaign has no Admin destination; " +
					"omitting Admin is the GM's tier, not the system's")
			}
		})

	t.Run("a reader with no campaign list gets no Campaigns section",
		func(t *testing.T) {
			t.Parallel()

			harness := newMounted(t, mount{handler: func(handler *play.Handler) {
				handler.Campaigns = nil
			}})

			got := harness.get(gmSlug, gmUserID)
			if got.status != http.StatusOK {
				t.Fatalf("GET /play with no campaign lister = %d, want %d: a nil "+
					"lister is a missing section, not a failed document",
					got.status, http.StatusOK)
			}

			if strings.Contains(got.body, "nav-campaigns") {
				t.Error("the document rendered a Campaigns section with no lister " +
					"behind it; a section above no links promises a destination it " +
					"does not have (UI §4.3)")
			}
		})
}

// addCampaign puts a campaign and its memberships straight into the fixture
// store, for the two documents no default fixture can produce: a campaign
// registered without a name, and one with no gameplay system.
//
// Directly rather than through `CreateCampaign`, because the fixture store's
// write methods answer "not supported" — they exist to satisfy the interface
// `campaigns.Resolve` needs, and a test that needed a second write path would
// be building a store instead of using one. The lock is the store's own: the
// document tests run in parallel with each other, and the map is shared by the
// resolution middleware.
func addCampaign(
	t *testing.T,
	harness *harness,
	campaign domain.Campaign,
	members map[int64]domain.Membership,
) {
	t.Helper()

	if campaign.Name == "" && campaign.Slug == "" {
		t.Fatal("a fixture campaign needs at least a slug: CampaignBySlug is the " +
			"only way Resolve finds it")
	}

	harness.store.mu.Lock()
	defer harness.store.mu.Unlock()

	harness.store.campaigns[campaign.Slug] = campaign
	if members != nil {
		harness.store.memberships[campaign.ID] = members
	}
}
