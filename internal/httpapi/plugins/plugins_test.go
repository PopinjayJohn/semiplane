package plugins_test

// The route's behaviour: the gates, the dispatch, and the two claims §10.6 makes about
// what a plugin's UI may and may not do.
//
// # What is asserted here and what is asserted in the plugin packages
//
// The split is by ownership, and it is worth stating because it is what makes each test
// findable. **`internal/web/plugins/dice` and `.../linkpreview` own what their markup
// says** — that a frame carries no field a result could arrive in, that a link preview
// refuses a private address. This file owns **what the route does with them**: that the
// actor is server-derived, that the dispatch is the hub's, that the rendered result is the
// `Resolution`, and that both routes are behind their gates.
//
// The one property that needs both sides is "renders the server's answer rather than a
// local one", and it is here rather than in `dice` because the *server's answer* is
// something only the hub produces: `TestTheRenderedResultIsTheHubsResolution`.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/httpapi/plugins"
	"github.com/semiplane/semiplane/internal/plugin"
	"github.com/semiplane/semiplane/internal/realtime"
	webplugins "github.com/semiplane/semiplane/internal/web/plugins"
	"github.com/semiplane/semiplane/internal/web/plugins/dice"
	"github.com/semiplane/semiplane/internal/web/plugins/linkpreview"
)

// --- The gates ---------------------------------------------------------------

// TestTheRollerIsBehindThePlayGateAndThePreviewBehindTheReadGate is ADR 0024 read off the
// mounted routes, in both directions.
//
// **Two claims, and the second is the one a rendering cannot show.** A player and an
// anonymous reader of a *public* campaign both clear `RequireRead`, so mounting both routes
// behind it would let a non-member roll and would pass every test that only looked at
// statuses for the page. So:
//
//   - the roller's `POST` refuses an anonymous reader and a non-member, because
//     `RequirePlay` is the gate `Mount` wraps it in;
//   - the preview's `GET` **answers for an anonymous reader of a public campaign**, because
//     `RequireRead` is the gate it is wrapped in and a link preview is a reading, not a
//     play (§10.6's row: "read-only").
//
// The second half is what proves the two gates are *different* rather than one gate spelled
// twice: a build that mounted both behind `RequirePlay` would fail the preview assertion,
// and one that mounted both behind `RequireRead` would fail the roller assertion.
func TestTheRollerIsBehindThePlayGateAndThePreviewBehindTheReadGate(t *testing.T) {
	t.Parallel()

	roller := campaignPath(testSlug)
	preview := previewPathFor(testSlug)

	t.Run("a roll for an anonymous reader of a public campaign is refused", func(t *testing.T) {
		t.Parallel()

		fixed := newHarness(t).withPlugins().withHub()

		recorder := fixed.post(roller, map[string]string{
			"placement": "p1",
			"expr":      "1d20",
			"seq":       "7",
		}, anonymousRequestor())

		if recorder.Code == http.StatusOK {
			t.Errorf("a roll for an anonymous reader answered %d; the roller dispatches an "+
				"intent and §10.6's authority is the player's, so it is behind RequirePlay. "+
				"Body: %q", recorder.Code, truncateBody(recorder.Body.String()))
		}

		if got := len(fixed.resolver.recorded()); got != 0 {
			t.Errorf("the resolver was called %d times for a reader who may not play; the "+
				"gate must run before the route, not inside it", got)
		}
	})

	t.Run("a roll for a non-member is refused", func(t *testing.T) {
		t.Parallel()

		fixed := newHarness(t).withPlugins().withHub()

		recorder := fixed.post(roller, map[string]string{
			"placement": "p1",
			"expr":      "1d20",
			"seq":       "7",
		}, domain.Requestor{Authenticated: true, UserID: 999, Username: "stranger"})

		if recorder.Code == http.StatusOK {
			t.Errorf("a roll for a non-member answered %d; §8.1 says no amount of public "+
				"visibility makes an anonymous visitor a player", recorder.Code)
		}

		if got := len(fixed.resolver.recorded()); got != 0 {
			t.Errorf("the resolver was called %d times for a non-member", got)
		}
	})

	t.Run("a preview for an anonymous reader of a public campaign answers", func(t *testing.T) {
		t.Parallel()

		fixed := newHarness(t).withPlugins().withPreview(http.DefaultClient)

		recorder := fixed.get(preview+"?url=", anonymousRequestor())

		// 204 rather than 200: the url parameter is empty, so the route has nothing to
		// fetch. What is being asserted is that the request **reached the handler** rather
		// than being turned away — a 401 or 403 here would mean the preview is behind the
		// play gate.
		if recorder.Code != http.StatusNoContent {
			t.Errorf("a preview for an anonymous reader of a public campaign answered %d, "+
				"want %d; the preview is a reading and belongs behind RequireRead, or every "+
				"reader of a public page loses their link previews. Body: %q",
				recorder.Code, http.StatusNoContent, truncateBody(recorder.Body.String()))
		}
	})
}

// TestAPluginRouteMountedWithoutItsGateRefuses is the direction ADR 0024's rule is about,
// and **404 is the right answer rather than 500** — which is worth stating because the first
// draft of this test asserted 500 and was wrong.
//
// A handler reached without `campaigns.Resolve` has nothing on its context: `AccessFrom`
// reports `TierNone`, and — because the gate is what supplies it — **no campaign either**, so
// `kindFor` has an empty slug and no registered pattern can match the request's path. The
// route therefore answers 404, which is:
//
//   - **indistinguishable from a URL that names nothing**, so a chain missing its gate cannot
//     be told apart from one where the route is absent;
//   - **not a disclosure**, because a reader who has not been admitted learns only that this
//     path does not exist, which is what S-8 asks for;
//
// where 500 would say "this path exists and we are broken", and a 500 that distinguishes a
// real route from a missing one is a small existence oracle about the build.
//
// The second half of the assertion is the one that matters: **the resolver was never
// called.** A route that fell through to a dispatch here would roll dice for a reader whose
// access was never decided.
func TestAPluginRouteMountedWithoutItsGateRefuses(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t).withPlugins().withHub()

	recorder := fixed.ungated(campaignPath(testSlug))

	if recorder.Code != http.StatusNotFound {
		t.Errorf("a GET on the ungated chain answered %d, want %d; a route mounted without "+
			"its access gate has no campaign on its context, and the answer must be the same "+
			"one a URL that names nothing gets",
			recorder.Code, http.StatusNotFound)
	}

	if got := len(fixed.resolver.recorded()); got != 0 {
		t.Errorf("the resolver was called %d times on the ungated chain; a dispatch for a "+
			"reader whose access was never decided is the failure this test exists for", got)
	}
}

// TestAnUnknownCampaignIsIndistinguishableFromAPrivateOne is S-8's "no access is 404",
// over this route.
//
// Two requests, two slugs, and **the assertion is byte-identity** — the status *and* the
// body. A private campaign and a campaign that does not exist must answer identically, or a
// 404 becomes an existence oracle (ADR 0024's stated reason).
func TestAnUnknownCampaignIsIndistinguishableFromAPrivateOne(t *testing.T) {
	t.Parallel()

	// Built from the pattern rather than written out, so a change to `dice.PagePath`
	// moves this assertion with it rather than leaving it testing a route that no longer
	// exists — which would make it a test that passes for the wrong reason.
	existing := campaignPath(testSlug)
	absentPath := campaignPath("nowhere")

	private := newHarness(t).withPlugins().withHub()
	private.visibility = domain.VisibilityPrivate

	absent := newHarness(t).withPlugins().withHub()

	// **The readers this is about, and why the GM and the player are not among them.**
	// S-8's equality is for a reader who is *not entitled*: a member of a private campaign
	// may read it, and a campaign that does not exist is not something they were entitled to
	// — so the two answers differ for them legitimately, and asserting they were identical
	// would be asserting a stronger rule than the record states. The oracle this guards is
	// the one an outsider could mount: "does this campaign exist?"
	for _, requestor := range []domain.Requestor{
		anonymousRequestor(),
		{Authenticated: true, UserID: 999, Username: "stranger"},
	} {
		privateResponse := private.get(existing, requestor)
		absentResponse := absent.get(absentPath, requestor)

		if privateResponse.Code != absentResponse.Code {
			t.Errorf("a private campaign answered %d and a campaign that does not exist "+
				"answered %d; S-8 requires them to be indistinguishable or a 404 confirms "+
				"the campaign exists", privateResponse.Code, absentResponse.Code)

			continue
		}

		if privateResponse.Body.String() != absentResponse.Body.String() {
			t.Errorf("a private campaign and a campaign that does not exist answered the "+
				"same status with different bodies:\n private: %q\n absent:  %q",
				privateResponse.Body.String(), absentResponse.Body.String())
		}
	}
}

// --- The actor ---------------------------------------------------------------

// TestTheActorCannotBeForgedFromRequestBytes is `realtime.Actor`'s doc comment made a
// test, from the one angle this route could plausibly have broken it.
//
// The claim is that nothing can populate an actor from request bytes. `Actor`'s fields are
// unexported **and** its `UnmarshalJSON` refuses, and this route builds one with
// `realtime.NewActor` from the access the gate resolved — so a form field, a query
// parameter and a header all have no path to it. Three requests, three smuggling attempts,
// and the assertion is that the recorded actor is the **gate's** every time.
//
// The three vectors are worth spelling out, because each is a different field of the frame
// and one of them is the field a client frame is *supposed* to carry: `campaign` and `role`
// name no field on `ClientIntent` at all (S-7.1's rule that a frame must not choose its own
// table), and `actor` is the sequence number, which is a `ClientSeq` and not an identity.
func TestTheActorCannotBeForgedFromRequestBytes(t *testing.T) {
	t.Parallel()

	t.Run("form fields naming a campaign, a role and an actor are ignored", func(t *testing.T) {
		t.Parallel()

		fixed := newHarness(t).withPlugins().withHub().applied(42)

		recorder := fixed.post("/c/"+testSlug+"/plugins/dice-roller", map[string]string{
			"placement": "p1",
			"expr":      "1d20",
			"seq":       "7",
			// The forgery attempt. Each of these is a field the wire has no name for, and
			// the route reads none of them.
			"campaign": "999",
			"role":     "gm",
			"actor":    "4242",
		}, playerRequestor())

		if recorder.Code != http.StatusOK {
			t.Fatalf("the roll answered %d, want 200: %q", recorder.Code,
				truncateBody(recorder.Body.String()))
		}

		assertActorIsTheGate(t, fixed.resolver.last(t), playerRequestor(), domain.RolePlayer)
	})

	t.Run("a query string naming a campaign and a role is ignored", func(t *testing.T) {
		t.Parallel()

		fixed := newHarness(t).withPlugins().withHub().applied(42)

		recorder := fixed.post(
			"/c/"+testSlug+"/plugins/dice-roller?campaign=999&role=gm",
			map[string]string{"placement": "p1", "expr": "1d20", "seq": "7"},
			playerRequestor())

		if recorder.Code != http.StatusOK {
			t.Fatalf("the roll answered %d, want 200: %q", recorder.Code,
				truncateBody(recorder.Body.String()))
		}

		assertActorIsTheGate(t, fixed.resolver.last(t), playerRequestor(), domain.RolePlayer)
	})

	t.Run("an actor built from the gate is the reader's own, not the GM's", func(t *testing.T) {
		t.Parallel()

		// The half that would catch a route that hard-coded a privileged identity: the
		// reader here is a **player**, and the resolution must be attributed to them. A
		// route that dispatched as the GM would make every player-authored roll an
		// audit-trail entry naming somebody else.
		fixed := newHarness(t).withPlugins().withHub().applied(42)

		recorder := fixed.post("/c/"+testSlug+"/plugins/dice-roller", map[string]string{
			"placement": "p1",
			"expr":      "1d20",
			"seq":       "7",
		}, playerRequestor())

		if recorder.Code != http.StatusOK {
			t.Fatalf("the roll answered %d, want 200: %q", recorder.Code,
				truncateBody(recorder.Body.String()))
		}

		recorded := fixed.resolver.last(t)
		if recorded.intent.Actor != realtime.UserID(playerUser) {
			t.Errorf("the resolution was attributed to user %d, want the reader's %d; a "+
				"plugin's dispatch is the account that loaded the page and nothing else",
				recorded.intent.Actor, playerUser)
		}

		assertActorIsTheGate(t, recorded, playerRequestor(), domain.RolePlayer)
	})
}

// assertActorIsTheGate asserts one recorded intent carries the identity the access gate
// resolved.
//
// **Three assertions and one of them is the important one.** Campaign, actor and role are
// all read from the gate — and the role assertion is the one that catches the interesting
// mistake, because a route that read the role from anywhere else would either dispatch as
// a GM for a player (a privilege escalation) or refuse a GM's roll (a broken table).
func assertActorIsTheGate(
	t *testing.T,
	recorded recordedIntent,
	requestor domain.Requestor,
	role domain.Role,
) {
	t.Helper()

	if recorded.intent.Campaign != testCampID {
		t.Errorf("the resolution ran against campaign %d, want %d; the campaign comes from "+
			"the URL the gate resolved and from nowhere else",
			recorded.intent.Campaign, testCampID)
	}

	if recorded.intent.Actor != realtime.UserID(requestor.UserID) {
		t.Errorf("the resolution ran as user %d, want %d", recorded.intent.Actor, requestor.UserID)
	}

	if recorded.intent.Role != role {
		t.Errorf("the resolution ran with role %q, want %q; the role comes from the "+
			"membership the gate looked up", recorded.intent.Role, role)
	}
}

// --- The dice roller ---------------------------------------------------------

// TestTheRenderedResultIsTheHubsResolution is §10.6's "renders the server's result" and
// §10.6's "it must not roll client-side", as one executable claim.
//
// **The version is the whole of it.** S-7.2 makes `version` the only ordering authority
// and ADR 0009 rule 2 says so; the answer arrives in the delta the hub broadcasts stamped
// with the version the hub assigned. So the test drives a resolution whose version is a
// number nothing in this request could have produced — not derivable from the expression, not
// sequential, not zero — and requires the rendered page to carry **exactly that number**.
//
// The three ways this test can fail, and each is a distinct defect:
//
//   - the page carries no version → the plugin invented a result;
//   - the page carries a different number → the plugin computed one;
//   - the page carries a version but not the placement → it read a frame it should not have.
func TestTheRenderedResultIsTheHubsResolution(t *testing.T) {
	t.Parallel()

	// A version chosen to be recognisable and unguessable: 8675309 is neither 0 nor 1 nor
	// anything a counter would produce, and it is the number a test can quote in a failure
	// message without the reader having to look anything up.
	const hubVersion = realtime.Version(8675309)

	fixed := newHarness(t).withPlugins().withHub().applied(hubVersion)

	recorder := fixed.post("/c/"+testSlug+"/plugins/dice-roller", map[string]string{
		"placement": "p1",
		"expr":      "1d20+5",
		"reason":    "Perception",
		"seq":       "7",
	}, playerRequestor())

	if recorder.Code != http.StatusOK {
		t.Fatalf("the roll answered %d, want 200: %q", recorder.Code,
			truncateBody(recorder.Body.String()))
	}

	body := recorder.Body.String()

	if !strings.Contains(body, `data-version="`+itoa(uint64(hubVersion))+`"`) {
		t.Errorf("the page does not carry the version the hub assigned (%d); §10.6 says "+
			"the roller renders the server's result, and a version is the only part of it "+
			"semiplane stamps itself. Body: %q", hubVersion, truncateBody(body))
	}

	if !strings.Contains(body, `data-placement="p1"`) {
		t.Errorf("the page does not carry the placement the hub moved; the roller must "+
			"render the frame it was given. Body: %q", truncateBody(body))
	}

	if !strings.Contains(body, `data-by="`+itoa(uint64(gmUser))+`"`) {
		t.Errorf("the page does not carry the account the resolution was attributed to; "+
			"the audit trail is the version *and* the actor. Body: %q", truncateBody(body))
	}

	// The negative half, and the one that matters: a page showing a number is only honest
	// if the number is not one the plugin made up. Nothing here is dice — there is no
	// `1d20+5 = 25` on the page, and asserting the *absence* of a plausible total is the
	// only way to catch a plugin that grew one. The strings are the obvious shapes a roll
	// would take.
	for _, invented := range []string{"total", "roll=25", ">25<", "rolled 25"} {
		if strings.Contains(strings.ToLower(body), invented) {
			t.Errorf("the page contains %q; the roller renders the server's stamped frame "+
				"and no number it did not receive. A number this package invented has no "+
				"version beside it, which is the whole of §10.6's rule. Body: %q",
				invented, truncateBody(body))
		}
	}
}

// TestTheRollerCrossesTheWireCodec is S-10.3's "identical authorisation and validation"
// made observable: the frame this plugin builds is decoded by `realtime.Decode`, so its
// grammar, its bounds and its refusal vocabulary are the wire's and not the plugin's.
//
// The observable is **which op and which arguments reached the resolver**. A plugin that
// built a `realtime.ClientIntent` in Go and handed it over without the codec would produce
// an identical `Intent` — so this test would pass for that implementation too, and the
// difference is a *structural* claim. What this test does hold is that the op is `roll`
// (the one §10.6 names) and that the arguments crossed as the wire's own typed struct
// rather than as an opaque blob, which is the half a struct hand-off would get wrong.
func TestTheRollerCrossesTheWireCodec(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t).withPlugins().withHub().applied(42)

	recorder := fixed.post("/c/"+testSlug+"/plugins/dice-roller", map[string]string{
		"placement": "p1",
		"expr":      "1d20+5",
		"reason":    "Perception",
		"seq":       "7",
	}, playerRequestor())

	if recorder.Code != http.StatusOK {
		t.Fatalf("the roll answered %d, want 200: %q", recorder.Code,
			truncateBody(recorder.Body.String()))
	}

	recorded := fixed.resolver.last(t)

	if recorded.intent.Frame.Op != realtime.Op(dice.OpRoll) {
		t.Errorf("the resolver saw op %q, want %q; §10.6's dice roller emits {\"op\":\"roll\"}",
			recorded.intent.Frame.Op, dice.OpRoll)
	}

	if recorded.intent.Frame.Seq != theNextSeq {
		t.Errorf("the resolver saw seq %d, want 7; the wire requires a seq so an answer can "+
			"be paired with the intent that asked for it", recorded.intent.Frame.Seq)
	}

	if recorded.intent.Frame.Args.Expr != "1d20+5" {
		t.Errorf(
			"the resolver saw expression %q, want %q",
			recorded.intent.Frame.Args.Expr,
			"1d20+5",
		)
	}

	if recorded.intent.Frame.Args.Reason != "Perception" {
		t.Errorf("the resolver saw reason %q, want %q", recorded.intent.Frame.Args.Reason,
			"Perception")
	}

	if recorded.intent.Frame.Args.Placement != realtime.PlacementID("p1") {
		t.Errorf("the resolver saw placement %q, want %q", recorded.intent.Frame.Args.Placement,
			"p1")
	}
}

// TestARefusalIsRenderedAsARefusalAndNotAsAFault is the two-shape argument in
// `dice.ReadRefusal`, from the route's side.
//
// **A refusal is a 200 with the reason on the page.** The reader's action was understood and
// answered — §10.6's roller renders the server's result, and "the Table declined" is a
// result. A 400 would tell a browser the form was malformed when the form was fine and the
// game said no.
//
// And the **machine reason is on `data-reason` while the prose is not it**: `not_your_turn`
// rendered as body text is a wire token on a page a person is reading, and §10.2 audits
// rendered documents.
func TestARefusalIsRenderedAsARefusalAndNotAsAFault(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t).withPlugins().withHub().refusing(realtime.RejectNotYourTurn)

	recorder := fixed.post("/c/"+testSlug+"/plugins/dice-roller", map[string]string{
		"placement": "p1",
		"expr":      "1d20",
		"seq":       "7",
	}, playerRequestor())

	if recorder.Code != http.StatusOK {
		t.Fatalf("a refused roll answered %d, want 200; the reader's action was understood "+
			"and answered. Body: %q", recorder.Code, truncateBody(recorder.Body.String()))
	}

	body := recorder.Body.String()

	if !strings.Contains(body, `data-reason="not_your_turn"`) {
		t.Errorf("the page does not carry the wire reason; §7.2's closed set of eight words "+
			"is the whole of what a client is told. Body: %q", truncateBody(body))
	}

	// The machine reason must appear **only** on the attribute. Checked over the parsed
	// tree's text nodes rather than by scanning the body for lines that do not contain
	// `data-reason=`, because that second form is what a substring check cannot do: a wire
	// token inside a text node and the same token inside an attribute value are the same
	// bytes on one line and different things to a reader. The DOM is where the difference
	// exists.
	audit := parseDocument(
		t,
		renderedDocument{where: "the refusal", status: recorder.Code, body: recorder.Body.Bytes()},
	)

	var walk func(*html.Node)

	walk = func(node *html.Node) {
		if node.Type == html.TextNode && strings.Contains(node.Data, "not_your_turn") {
			t.Errorf("the wire reason appears in a text node (%q); a machine token on a page "+
				"a person reads is not copy, and §10.2 audits rendered documents",
				strings.TrimSpace(node.Data))
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(audit.root)

	if !strings.Contains(body, "not this reader") {
		t.Errorf("the page does not explain the refusal in prose; the reader needs a "+
			"sentence, not a token. Body: %q", truncateBody(body))
	}
}

// TestAResolverFaultIsNotRenderedAsARefusal is the half of the previous test that a
// rendering cannot show, and it is the one that matters.
//
// **A fault must not become a rule.** If `RefusalOutcome` read any error as a refusal, a
// resolver that panicked would render "it is not this reader's turn" — a claim about the
// campaign's rules that nobody established, to a reader who would act on it. So the route is
// given an error that is **not** a refusal and the page must not carry a reason.
func TestAResolverFaultIsNotRenderedAsARefusal(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t).withPlugins().withHub().
		refusingWith(errors.New("the resolver exploded"))

	recorder := fixed.post("/c/"+testSlug+"/plugins/dice-roller", map[string]string{
		"placement": "p1",
		"expr":      "1d20",
		"seq":       "7",
	}, playerRequestor())

	body := recorder.Body.String()

	if strings.Contains(body, "data-reason=") {
		t.Errorf("the page carries a refusal reason for a resolver that failed rather "+
			"than refused: %q; a fault rendered as a rule is a claim about the campaign "+
			"nobody established", truncateBody(body))
	}

	if strings.Contains(body, "data-roll-refused") {
		t.Errorf("the page rendered the refusal state for a resolver that failed: %q",
			truncateBody(body))
	}
}

// TestARollWithNoTokenIsRefusedBeforeTheHubIsReached is the ordering claim in `roll`'s
// doc comment, and it is a claim about **the hub**, not about the status.
//
// A roll with no placement cannot be resolved — `rules.NewMutation` requires a target and a
// roll names no object of its own — so the hub would refuse it with `invalid_args`. But a
// refusal *from the adapter* would put a malformed attempt into the campaign's roll log, and
// the reader gets a codec class rather than a sentence naming the field. So the route
// refuses it before dispatching, and the observable is that the resolver was never called.
func TestARollWithNoTokenIsRefusedBeforeTheHubIsReached(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name string
		form map[string]string
	}{
		{
			name: "no token at all",
			form: map[string]string{"expr": "1d20", "seq": "7"},
		},
		{
			name: "a token that is only whitespace",
			form: map[string]string{"placement": "   ", "expr": "1d20", "seq": "7"},
		},
		{
			name: "a sequence number that is not a number",
			form: map[string]string{"placement": "p1", "expr": "1d20", "seq": "later"},
		},
		{
			name: "a sequence number that is negative",
			form: map[string]string{"placement": "p1", "expr": "1d20", "seq": "-1"},
		},
		{
			name: "a sequence number past the wire's counter range",
			form: map[string]string{
				"placement": "p1", "expr": "1d20", "seq": "9007199254740993",
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			fixed := newHarness(t).withPlugins().withHub()

			recorder := fixed.post("/c/"+testSlug+"/plugins/dice-roller",
				testCase.form, playerRequestor())

			if recorder.Code != http.StatusBadRequest {
				t.Errorf("the roll answered %d, want %d: %q", recorder.Code,
					http.StatusBadRequest, truncateBody(recorder.Body.String()))
			}

			if got := len(fixed.resolver.recorded()); got != 0 {
				t.Errorf("the resolver was called %d times for a request this route refuses "+
					"on its own shape; a malformed attempt must not reach the campaign's "+
					"roll log", got)
			}
		})
	}
}

// TestARollWithoutAHubSaysSoRatherThanRenderingAFormThatDoesNothing is the wiring-fault
// claim: 503, not a silent success.
//
// A form whose button does nothing is a product reporting itself healthy, and a client that
// got a 200 with no answer would have no way to find out why.
func TestARollWithoutAHubSaysSoRatherThanRenderingAFormThatDoesNothing(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t).withPlugins()

	recorder := fixed.post("/c/"+testSlug+"/plugins/dice-roller", map[string]string{
		"placement": "p1", "expr": "1d20", "seq": "7",
	}, playerRequestor())

	if recorder.Code != http.StatusServiceUnavailable {
		t.Errorf("a roll with no hub wired answered %d, want %d; a form that renders and "+
			"then does nothing is a broken product reporting itself working",
			recorder.Code, http.StatusServiceUnavailable)
	}
}

// TestTheRollResultIsNotCacheable is AGENTS.md's gate-response rule, applied to a body that
// is worse than a gate response: it is campaign state.
//
// A cache that stored "at version 4" and served it to a reader whose token is now at
// version 9 has shown them a number the server would never produce — and the *page* is the
// authority, so a stale page is a false audit trail rather than a stale picture.
func TestTheRollResultIsNotCacheable(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t).withPlugins().withHub().applied(42)

	recorder := fixed.post("/c/"+testSlug+"/plugins/dice-roller", map[string]string{
		"placement": "p1", "expr": "1d20", "seq": "7",
	}, playerRequestor())

	if got := recorder.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Errorf("the roll result carried Cache-Control %q, want %q; a body carrying a "+
			"version and an actor must never be stored by something that does not know "+
			"who asked", got, "private, no-store")
	}
}

// TestTheWidgetRendersTheCampaignSystemsNotation is §10.4's "the table is data" reaching
// the interface: the notation on the page came from a gameplay module.
//
// The assertion is that the **5e engine's own example** appears, read through the engine
// rather than a fixture — so a build where the plugin hard-coded `1d20` would fail here.
func TestTheWidgetRendersTheCampaignSystemsNotation(t *testing.T) {
	t.Parallel()

	// The real grammar, read through the real engine. A fixture spelling `1d20` would make
	// this pass whether or not the wiring put the system's notation on the page — and that
	// is the whole claim: §10.4 says the notation is data, so a plugin that hard-coded a die
	// would be the defect this catches.
	grammar := rulesGrammar()

	if !grammar.Valid() {
		t.Fatalf("the 5e grammar is not valid, so there is nothing to assert: %+v", grammar)
	}

	// **Three states, and the two absences are half the test.** With a system, the notation
	// is on the page. With no `Systems` at all, and with one that fails, the page still
	// renders and carries no notation — which is S-10.6's first row reached from the UI
	// side and §14's "renders for an empty output". A widget that 500'd over a missing
	// grammar would take a dice roller down over a cosmetic field.
	for _, testCase := range []struct {
		name        string
		build       func(*harness) *harness
		wantExample bool
	}{
		{
			name:        "the campaign's system declares a notation",
			build:       func(h *harness) *harness { return h.withSystems() },
			wantExample: true,
		},
		{
			name:        "no gameplay systems are wired at all",
			build:       func(h *harness) *harness { return h.withNoSystem() },
			wantExample: false,
		},
		{
			name:        "the campaign's system cannot be resolved",
			build:       func(h *harness) *harness { return h.withBrokenSystem() },
			wantExample: false,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			fixed := testCase.build(newHarness(t).withPlugins().withHub())

			recorder := fixed.get(campaignPath(testSlug), playerRequestor())

			if recorder.Code != http.StatusOK {
				t.Fatalf("the widget answered %d, want 200: %q", recorder.Code,
					truncateBody(recorder.Body.String()))
			}

			body := recorder.Body.String()

			if testCase.wantExample {
				if !strings.Contains(body, `data-testid="roll-notation"`) {
					t.Errorf("the widget carries no notation hook; §10.4 says the notation " +
						"is the system's data and a reader cannot see what they are typing " +
						"without it")
				}

				// **Over the parsed document, not over the bytes.** `html/template` escapes
				// `+` as `&#43;` in text — it defends against the old UTF-7 attack and the
				// cost is that `1d20+2` appears in the response as `1d20&#43;2`. Comparing
				// the raw bytes would fail on a correct page and pass on a wrong one, which
				// is worse than not asserting at all.
				audit := parseDocument(t, renderedDocument{
					where:  testCase.name,
					status: recorder.Code,
					body:   recorder.Body.Bytes(),
				})

				rendered := textOf(findAll(audit.root, anyTag)[0])
				if !strings.Contains(rendered, grammar.Example) {
					t.Errorf("the widget does not carry the system's own worked example (%q); "+
						"the notation on the page must be the system's, or a plugin has "+
						"hard-coded one. Rendered text: %q", grammar.Example, rendered)
				}

				return
			}

			if strings.Contains(body, `data-testid="roll-notation"`) {
				t.Errorf("the widget renders a notation hook with no system behind it; §14 " +
					"requires an empty output to render, not to be filled with a guess")
			}
		})
	}
}

// TestTheWidgetRendersBeforeAnyRoll is §14's "every declared view renders for an empty
// `Derive` output", applied to a page type.
//
// The widget is the page **before** any roll, so the empty outcome is its normal state and
// not an edge case. The assertion is that the form is there and that neither result state
// is — a page that rendered the result block with nothing in it would be a control
// announcing a roll that did not happen.
func TestTheWidgetRendersBeforeAnyRoll(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t).withPlugins().withHub()

	recorder := fixed.get("/c/"+testSlug+"/plugins/dice-roller", playerRequestor())

	if recorder.Code != http.StatusOK {
		t.Fatalf("the widget answered %d, want 200: %q", recorder.Code,
			truncateBody(recorder.Body.String()))
	}

	body := recorder.Body.String()

	for _, required := range []string{
		`data-testid="roll-form"`,
		`data-testid="roll-submit"`,
		`name="placement"`,
		`name="expr"`,
		`name="seq"`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("the widget carries no %s; the page before any roll is the roller's "+
				"normal state", required)
		}
	}

	for _, absent := range []string{`data-testid="roll-result"`, `data-testid="roll-refused"`} {
		if strings.Contains(body, absent) {
			t.Errorf("the widget renders %s before any roll; a result block with nothing "+
				"in it is a control announcing a roll that did not happen", absent)
		}
	}
}

// --- The link preview --------------------------------------------------------

// TestALinkIntoACampaignsOwnContentRootIsNotFetched is the security boundary, and it is
// written as "is not fetched" rather than "is refused with a status" because the *fetch* is
// what must not happen.
//
// A campaign's pages live on this host. A URL naming this host names a campaign page. So a
// helper that fetched whatever a page linked to would fetch `/c/greyhaven/wiki/Vault` and
// hand a private page's rendered HTML to whoever wrote the link — S-8's access matrix
// answered through a path that never went through a gate.
//
// The server below counts every request it receives, and the assertion is that the counter
// is **zero**. That is stronger than "the response was a refusal": a helper that fetched
// and then refused to *render* would still have leaked the bytes into this process.
func TestALinkIntoACampaignsOwnContentRootIsNotFetched(t *testing.T) {
	t.Parallel()

	// A server standing in for "this host", with a page that would be the private one.
	var served int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served++

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// The status line is committed and the handler has nothing else to do; the
		// counter above is the observation this test makes.
		_, _ = w.Write([]byte(`<html><head><title>THE PASSPHRASE IS HUNTER2</title></head>` +
			`<body><p>the vault</p></body></html>`))
	}))
	t.Cleanup(server.Close)

	// The **guarded** unfurler, not `withPreview`, and the difference is the whole point of
	// the assertion below: `NewOver` disables the address policy because a test needs an
	// `httptest.Server`, so a route under that seam would fetch this URL and pass the count
	// for a reason that has nothing to do with the route. `New` is what `cmd/server` wires,
	// and `checkAddress` refuses a typed loopback host before any I/O — so the counter at the
	// bottom stays zero with no fetch, no response and no parse.
	fixed := newHarness(t).withPlugins().withGuardedPreview()

	for _, target := range []string{
		"http://" + server.Listener.Addr().String() + "/c/greyhaven/wiki/Vault",
		"http://" + server.Listener.Addr().String() + "/c/greyhaven/wiki/Secrets",
	} {
		recorder := fixed.get(
			"/c/"+testSlug+"/plugins/link-preview?url="+queryEscape(target),
			playerRequestor())

		if recorder.Code != http.StatusNoContent {
			t.Errorf("a link into the campaign's own content root (%s) answered %d, want %d; "+
				"the helper has no %T and no filesystem handle, so there is no code path by "+
				"which it can read a page of the campaign it runs in",
				target, recorder.Code, http.StatusNoContent, plugins.Handler{})
		}

		if strings.Contains(recorder.Body.String(), "HUNTER2") {
			t.Errorf("the response carried the private page's title for %s; the preview "+
				"returns three fields from the REMOTE page and this one is not remote",
				target)
		}
	}

	if served != 0 {
		t.Errorf("the server received %d requests; a link naming this host must be refused "+
			"before any fetch, because the bytes it would return are campaign content", served)
	}
}

// TestAPreviewRequestWithNoUrlIsNotAnError is the route's degradation contract: a link that
// cannot be previewed answers 204 with no body.
//
// The reader needs a plain link either way, and a page whose links turn into error messages
// when a fetch fails is a page that reports its own infrastructure to everybody.
func TestAPreviewRequestWithNoUrlIsNotAnError(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t).withPlugins().withPreview(http.DefaultClient)

	recorder := fixed.get("/c/"+testSlug+"/plugins/link-preview", playerRequestor())

	if recorder.Code != http.StatusNoContent {
		t.Errorf("a preview with no url answered %d, want %d", recorder.Code, http.StatusNoContent)
	}

	if recorder.Body.Len() != 0 {
		t.Errorf("a preview with no url carried a body: %q", recorder.Body.String())
	}
}

// TestAPreviewRequestIsNotCacheable is AGENTS.md's rule once more, for the preview: the
// fetch happened for the reader who asked.
//
// A shared cache serving one reader's preview to another is a fetch on their behalf — and
// for a link inside a private campaign, a disclosure of which links that campaign's pages
// cite.
func TestAPreviewRequestIsNotCacheable(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t).withPlugins().withPreview(http.DefaultClient)

	recorder := fixed.get("/c/"+testSlug+"/plugins/link-preview", playerRequestor())

	if got := recorder.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Errorf("the preview carried Cache-Control %q, want %q", got, "private, no-store")
	}
}

// TestAPreviewWithNoUnfurlerWiredSaysSo is the wiring fault for the preview: 503.
//
// A build that registers the hook and forgets the unfurl helper would otherwise render a
// card with nothing in it — or, worse, a card that looks like a preview and is not.
func TestAPreviewWithNoUnfurlerWiredSaysSo(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t).withPlugins()

	recorder := fixed.get("/c/"+testSlug+"/plugins/link-preview?url=https://example.com/",
		playerRequestor())

	if recorder.Code != http.StatusServiceUnavailable {
		t.Errorf("a preview with no unfurler wired answered %d, want %d; a card that looks "+
			"like a preview and is not is worse than an honest refusal",
			recorder.Code, http.StatusServiceUnavailable)
	}
}

// --- The mount ---------------------------------------------------------------

// TestAMountedRouteDerivesItsPatternsFromTheRegistry is the "one answer per route" claim.
//
// The route patterns come from `webplugins.Registry`, not from literals in the route
// package — so **a page type registered under a path the route does not know is mounted
// anyway.** That is what makes "naming a route is a matter of registering it" true rather
// than nearly true, and it is the only assertion that can tell a derived mount from a
// copied one: a route with the pattern in a literal would need an edit to reach a new page
// type, and this test registers one and asks for it.
func TestAMountedRouteDerivesItsPatternsFromTheRegistry(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t).withPlugins()

	// A second page type, on a path the route package has never heard of.
	const (
		newKind  = "spare-dice-tray"
		newPath  = "/c/{slug}/plugins/spare-dice-tray"
		newTitle = "Spare dice tray"
	)

	if err := fixed.ui.Register(webplugins.Declaration{
		Name:  "spare-tray",
		Title: "Spare tray",
		PageTypes: []webplugins.PageType{{
			Kind:   newKind,
			Title:  newTitle,
			Path:   newPath,
			Render: dice.Widget,
		}},
		Emits: []rules.Op{dice.OpRoll},
	}); err != nil {
		t.Fatalf("registering the spare tray: %v", err)
	}

	recorder := fixed.get("/c/"+testSlug+"/plugins/spare-dice-tray", playerRequestor())

	if recorder.Code != http.StatusOK {
		t.Errorf("a page type registered after the route was built answered %d, want %d; "+
			"the mount is derived from the registry, so registering is what mounts. "+
			"Body: %q", recorder.Code, http.StatusOK, truncateBody(recorder.Body.String()))
	}

	if !strings.Contains(recorder.Body.String(), newTitle) {
		t.Errorf("the spare tray's own title is not on the page; the document's title comes "+
			"from the registered page type. Body: %q", truncateBody(recorder.Body.String()))
	}
}

// TestTheDicePluginDeclaresOnlyOpsASystemResolves is §10.6's "it cannot register a new op"
// as the route's precondition.
//
// The declaration is checked at registration, so this is really a test that the *fixture*
// is honest: the roller emits `roll`, `dnd5e` resolves `roll`, and a plugin declaring
// something else is refused. The second half is the part that matters — it is the negative
// control, and without it "the roller registered" proves nothing.
func TestTheDicePluginDeclaresOnlyOpsASystemResolves(t *testing.T) {
	t.Parallel()

	ui := webplugins.New(systems(t))

	if err := ui.Register(dice.Declaration()); err != nil {
		t.Fatalf("the dice roller refused registration against the real 5e engine: %v", err)
	}

	// The negative control. A UI plugin emitting an op no system resolves is refused, and
	// the sentinel is `plugin.ErrNotAnOp` — the tier violation — rather than
	// `webplugins.ErrMalformedUIPlugin`, because a well-formed declaration of an operation
	// is *forbidden*, not malformed.
	err := ui.Register(webplugins.Declaration{
		Name:  "smuggled-op",
		Title: "Smuggled op",
		Emits: []rules.Op{"set_hp_everywhere"},
		PageTypes: []webplugins.PageType{{
			Kind:   "smuggled",
			Title:  "Smuggled",
			Path:   "/c/{slug}/plugins/smuggled",
			Render: dice.Widget,
		}},
	})
	if err == nil {
		t.Fatalf("a plugin declaring an op no system resolves was registered; §10.3 says a " +
			"UI plugin may only emit operations some gameplay system already resolves")
	}

	if !errors.Is(err, plugin.ErrNotAnOp) {
		t.Errorf("the refusal was %v, want one wrapping plugin.ErrNotAnOp; a boot pass "+
			"counts tier violations with one question", err)
	}

	if errors.Is(err, webplugins.ErrMalformedUIPlugin) {
		t.Errorf("the refusal also satisfies ErrMalformedUIPlugin; a declaration emitting " +
			"an operation is well formed and forbidden, and a boot pass counting " +
			"malformed registrations would then include a plugin author trying to define " +
			"one")
	}
}

// TestTheDicePluginDeclaresAnOperationAndNothingElse is the declaration's shape, read as a
// table.
//
// **Every field is asserted**, because a declaration is one decision and a caller who
// registered a page type and then failed to register its hook has built half a plugin. The
// `Emits` row is the one §10.6's table is about, and `PageTypes` the one that makes the
// route reachable.
func TestTheDicePluginDeclaresAnOperationAndNothingElse(t *testing.T) {
	t.Parallel()

	declared := dice.Declaration()

	if declared.Name != dice.PluginName || declared.Title == "" {
		t.Errorf("the declaration identifies itself as %q/%q; §10.2 requires both halves "+
			"and webplugins.ErrNoName and ErrNoTitle refuse them at registration",
			declared.Name, declared.Title)
	}

	if len(declared.RenderHooks) != 0 || len(declared.Observers) != 0 {
		t.Errorf("the roller declared %d render hooks and %d observers; §10.6's dice row "+
			"is a page type and an emitted operation, and an observer whose output no client "+
			"can reach is a declaration nobody can act on",
			len(declared.RenderHooks), len(declared.Observers))
	}

	if len(declared.Emits) != 1 || declared.Emits[0] != dice.OpRoll {
		t.Errorf("the roller declares Emits %v, want exactly [%s]", declared.Emits, dice.OpRoll)
	}

	if len(declared.PageTypes) != 1 {
		t.Fatalf("the roller declares %d page types, want 1", len(declared.PageTypes))
	}

	if pageType := declared.PageTypes[0]; pageType.Path != dice.PagePath ||
		pageType.Kind != dice.Kind || pageType.Title == "" {
		t.Errorf("the page type is %+v; its path is the route it is mounted at and its kind "+
			"is the game object kind a page of this kind claims", pageType)
	}
}

// TestTheLinkPreviewDeclaresAReadOnlyHook is §10.6's link-preview row, read as a
// declaration.
//
// **`Emits` is empty and `PageTypes` is empty**, and both absences are the claim: this
// plugin dispatches nothing and owns no route of its own. A page type would be a route
// serving a document with one link on it; an `Emits` entry would need a gameplay system to
// back it, and "read-only" is the row.
func TestTheLinkPreviewDeclaresAReadOnlyHook(t *testing.T) {
	t.Parallel()

	declared := linkpreview.Declaration()

	if len(declared.Emits) != 0 {
		t.Errorf("the link preview declares Emits %v; §10.6's row for it is read-only and "+
			"the dispatching row is the dice roller's", declared.Emits)
	}

	if len(declared.PageTypes) != 0 || len(declared.Observers) != 0 {
		t.Errorf("the link preview declares %d page types and %d observers; it is a render "+
			"hook, and a route serving a document with one link on it would be a page "+
			"whose entire content is a preview",
			len(declared.PageTypes), len(declared.Observers))
	}

	if len(declared.RenderHooks) != 1 {
		t.Fatalf("the link preview declares %d render hooks, want 1", len(declared.RenderHooks))
	}

	hook := declared.RenderHooks[0]

	if hook.Name != linkpreview.HookName {
		t.Errorf("the hook is named %q, want %q; the name is what a status page prints and "+
			"what Registry.RenderHook answers for", hook.Name, linkpreview.HookName)
	}

	if hook.Kind != "" {
		t.Errorf("the hook is scoped to kind %q; an external link appears in prose, in a "+
			"stat block and in a journal entry, so scoping it to one kind would be scoping "+
			"it to the one where links happen to be common", hook.Kind)
	}
}

// TestTheLinkPreviewRegistersWithNoOpsBecauseItDispatchesNone is the registration-time
// consequence of the previous test, and it is here so the two halves are one claim.
//
// The registry checks every declared operation against the registered systems; with none
// declared there is nothing to check, and the plugin is admitted. A negative control
// elsewhere (`TestTheDicePluginDeclaresOnlyOpsASystemResolves`) covers the other direction.
func TestTheLinkPreviewRegistersWithNoOpsBecauseItDispatchesNone(t *testing.T) {
	t.Parallel()

	ui := webplugins.New(systems(t))

	if err := ui.Register(linkpreview.Declaration()); err != nil {
		t.Fatalf("the link preview refused registration: %v", err)
	}

	if _, declared := ui.RenderHook(linkpreview.HookName); !declared {
		t.Errorf("the registered hook is not findable by name; Registry.RenderHook is how " +
			"the pipeline's owner reaches it")
	}
}

// --- Helpers -----------------------------------------------------------------

// campaignPath is the dice roller's URL for one campaign slug.
//
// **Derived from the plugin's own pattern**, and that is the point: a test that spelled
// `/c/greyhaven/plugins/dice-roller` as a literal would keep passing after the pattern
// changed — and a test that keeps passing after the route it names has moved is a test that
// has stopped testing anything.
func campaignPath(slug string) string {
	return pathForPattern(slug, dice.PagePath)
}

// previewPathFor is the link preview's URL for one campaign slug, derived the same way.
func previewPathFor(slug string) string {
	return pathForPattern(slug, linkpreview.PreviewPath)
}

// pathForPattern substitutes a campaign slug into one mux pattern.
//
// **The `after` half of the cut and not the `before` half**, because the placeholder is in
// the middle: `/c/{slug}/plugins/dice-roller` becomes `/c/` + slug + `/plugins/dice-roller`.
// Taking the wrong half produces `/c/greyhaven/c`, which matches `/c/{slug}/` and so
// reaches the campaign gate while never reaching the route — a failure that reads as "the
// route is broken" rather than as "the test built the wrong URL", and which is worth the
// helper being explicit about.
func pathForPattern(slug, pattern string) string {
	_, after, found := strings.Cut(pattern, "{slug}")
	if !found {
		return pattern
	}

	return "/c/" + slug + after
}

// itoa renders a number for an attribute assertion.
//
// `strconv.FormatUint` rather than `fmt.Sprintf`, because the comparison is against an
// attribute value and `fmt` would be the larger import for a narrower need.
func itoa(value uint64) string {
	return strconv.FormatUint(value, 10)
}

// queryEscape percent-encodes a URL for a query parameter.
//
// `url.QueryEscape` and not `url.PathEscape`: the value goes in a query, and a `?` or `&`
// inside it must be escaped or it becomes syntax.
func queryEscape(value string) string {
	return url.QueryEscape(value)
}

// rules is referenced by two tests that build a declaration; the alias keeps the import
// list readable at each use.
var _ = rules.Query{}
