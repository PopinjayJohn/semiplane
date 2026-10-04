package main

// `newPlayRoute` hands the play document its three seams.
//
// # Why this file exists
//
// Phase 11's demo-vault suite found that `newPlayRoute` built a `play.Handler` with
// **only** `Hub` and `Logger`, while `play.Handler` also declares `Campaigns`, `Systems`
// and `Snapshot`. Phase 9 added those fields so the document could render the campaign
// switcher, the die sheet and the token list, and the wiring was never updated.
//
// The product consequence: **every campaign rendered §4.7's empty state for all three** —
// a die sheet with "no roll notation", a token list reading "No tokens have been placed
// yet" — including `greyhaven` and its three seeded placements.
//
// # Why nothing was red
//
// Each field's doc comment says a nil value renders the empty state. That is the correct
// contract, and it is exactly what made the defect invisible: **an unwired field is
// indistinguishable from a campaign that genuinely has nothing to show.** So a test
// asserting "the token list is empty" passes in both worlds, and the field being unset
// cannot be observed from the empty state at all.
//
// These tests therefore assert on **the seams themselves**, not on what the empty state
// looks like. That is the only place the difference is visible, and asserting the
// rendered empty state would be asserting the thing that hid the bug.
//
// # What each assertion is for
//
//   - `Systems` nil → the die sheet has no notation **on every campaign**, including one
//     whose system this build resolves. A nil here is not a degraded mode; it is a
//     permanent one.
//   - `Snapshot` nil → the first server-rendered document never shows placements, which
//     is the render *before* the socket delivers the snapshot and therefore the only
//     render where the server has anything to say.
//   - `Campaigns` nil → the navigation's campaign switcher is empty, so a member of
//     three campaigns is shown none of them.
//
// Each is checked against **the field**, and each is mutation-tested below, because a
// nil check on a field nothing reads is a check on a constant.

import (
	"context"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/domain/systems/dnd5e"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/httpapi/play"
	"github.com/semiplane/semiplane/internal/realtime"
)

// TestThePlayRouteIsWiredWithEverySeamTheDocumentDeclares is the wiring claim.
//
// **Derived from the type, not from a list.** It reads `play.Handler`'s own fields
// through reflection and requires each function-typed seam to be non-nil, so a field
// added by a later phase fails here until it is wired — the same derivation the
// accessibility gate and `demo-check` use, and for the same reason: a hand-written
// list is a checklist describing fields that no longer exist.
func TestThePlayRouteIsWiredWithEverySeamTheDocumentDeclares(t *testing.T) {
	// No `t.Parallel()`: ADR 0004, a `*store.Store` is a process-wide slot and
	// `newInstance` opens one.
	handler := wiredPlayHandler(t)

	// **Reflected off the type, not from a list.** A hand-written list is a checklist
	// describing fields that no longer exist, and this repository has records of one: a
	// route package holding 23 tests and matching none of them, `A11Y_PKGS` naming
	// three packages, and a sheet list outliving the field it named. So the set of
	// seams comes from `play.Handler` itself.
	want := make(map[string]struct{})

	seamType := reflect.TypeFor[play.Handler]()

	for _, field := range reflect.VisibleFields(seamType) {
		// A seam is a field this route can be **left unwired**: a function the
		// composition root must supply, or an interface it must satisfy.
		//
		// **An interface counts, and the first version of this test missed one.**
		// It looked only for function fields with parameters and found `Snapshot` and
		// `Systems` — reporting 2 seams and passing, while `Campaigns` (an interface)
		// sat unwatched. So a derivation that quietly under-counts is the same defect
		// as a hand-written list, one level up: it looked like the registry-derived
		// kind check while answering a different question.
		//
		// `Hub` and `Logger` are pointers and the two time bounds are integers, so
		// neither shape matches them. A function with **no** parameters is a method
		// value rather than a lookup seam, so it is excluded for the same reason.
		switch field.Type.Kind() {
		case reflect.Interface:
		case reflect.Func:
			if field.Type.NumIn() == 0 {
				continue
			}
		default:
			continue
		}

		want[field.Name] = struct{}{}
	}

	if len(want) == 0 {
		t.Fatal("`play.Handler` declares no function or interface field, so this test " +
			"has nothing to check. Either the type lost its seams or the reflection is " +
			"wrong, and both mean the claim below is vacuous")
	}

	value := reflect.ValueOf(*handler)

	for _, name := range sortedNames(want) {
		field := value.FieldByName(name)
		if !field.IsValid() {
			t.Errorf("`play.Handler` has no field %q", name)

			continue
		}

		// `IsNil` is valid on both shapes above and panics on neither, which is why
		// the switch above admits exactly these two kinds.
		if field.IsNil() {
			t.Errorf("play.Handler.%s is nil, so the document renders §4.7's empty "+
				"state for every campaign — which is not a degraded mode but a "+
				"permanent one. The field's own doc comment says nil means the empty "+
				"state, so nothing else in the product can tell an unwired field from "+
				"a campaign with nothing to show; this is the only place it is visible",
				name)
		}
	}

	// The count is logged rather than asserted against a literal: a literal here would
	// be the checklist this file argues against, and adding a seam should not fail a
	// test about wiring.
	t.Logf("`play.Handler` declares %d unwirable seam(s): %v",
		len(want), sortedNames(want))
}

// TestThePlayRouteSeesTheCampaignsItIsGiven is narrower and catches a different fault:
// a seam that is non-nil but wired to the wrong thing.
//
// The composition-root defect and the wrong-seam defect look identical in the rendered
// document and are separated only here.
func TestThePlayRouteSeesTheCampaignsItIsGiven(t *testing.T) {
	handler := wiredPlayHandler(t)

	if handler.Campaigns == nil {
		t.Fatal("Campaigns is nil. The navigation's campaign switcher lists the " +
			"campaigns a reader is a member of, and an unwired seam shows a member " +
			"of three campaigns none of them")
	}

	// Asserting the **interface shape** rather than making a call is deliberate. The
	// question this file answers is *which* lister was wired, and a call would also
	// pass for any lister that happens to answer — including one scoped to the whole
	// instance, which is the mistake `play.CampaignLister`'s doc comment warns about
	// by name: "a handler that could list every campaign on the instance could be asked
	// about a campaign the reader has no business knowing exists".
	if _, ok := any(handler.Campaigns).(membershipLister); !ok {
		t.Error("the wired Campaigns does not expose CampaignsForUser, so it is not " +
			"the membership-scoped lister the interface names")
	}
}

// registerCampaignUnderSystem registers one campaign naming a **resolvable** system.
//
// `instance.registerCampaign` hardcodes `SystemID: "5e-2024"`, and that is a **ruleset**
// id — the fingerprint's own format is `system=5e;ruleset=5e-2024;…` — so a campaign it
// registers names a gameplay system this build does not resolve. That is fine for the
// tests that want a campaign and fine for the degraded-path tests, and it is exactly
// wrong for a test about the `Systems` seam, which has to answer.
//
// So the seam tests register their own campaign rather than changing the shared fixture.
// **Changing the fixture would be the tempting move and the wrong one**: `5e-2024` is
// load-bearing for whatever else reads that column, and silently re-pointing it would
// make this test pass by editing the evidence.
//
// Noted as a finding rather than fixed: a fixture whose `system_id` names a *ruleset* is
// a trap for the next test that asks what system a campaign plays.
func registerCampaignUnderSystem(
	t *testing.T, inst *instance, slug string, system rules.ID,
) domain.Campaign {
	t.Helper()

	registrar := campaigns.NewRegistrar(inst.store, filepath.Join(inst.base, "vaults"), nil)

	campaign, err := registrar.Register(t.Context(), campaigns.RegisterRequest{
		Slug:           slug,
		Name:           slug,
		SystemID:       string(system),
		RulesetVersion: "v1",
		Visibility:     domain.VisibilityPublic,
		OwnerID:        inst.owner.ID,
	})
	if err != nil {
		t.Fatalf("register campaign %s under system %q: %v", slug, system, err)
	}

	return campaign
}

// wiredPlayHandler builds the route over a real plane, through the product's own
// constructors.
//
// **The plane is built here rather than taken from `instance.serve`,** because
// `serve` assembles the whole content pipeline — which needs the fixture's campaigns
// registered and a watcher over each content root — and this file is about three
// fields on a struct literal. Going through the pipeline would make a wiring assertion
// depend on the content watcher, so a failure here would be about a watcher and not
// about the wiring.
//
// The plane is still the product's, over the fixture's real store and the real
// `plugin.NewResolver`: `newRealtimePlane` is the composition root's own constructor,
// so this is the same object `runServer` builds, not a stand-in for it. ADR 0004 means
// the store is a process-wide slot, so this cannot be `t.Parallel()`.
func wiredPlayHandler(t *testing.T) *play.Handler {
	t.Helper()

	return wiredPlayHandlerOver(t, newInstance(t))
}

// wiredPlayHandlerOver builds the route over a fixture the caller owns, so a test
// that needs a campaign registered can register one first.
func wiredPlayHandlerOver(t *testing.T, inst *instance) *play.Handler {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	inst.plane = newRealtimePlane(ctx, inst.store, inst.registry, inst.plugins, discardLogger())

	t.Cleanup(func() {
		closeRealtimePlane(t.Context(), inst.plane, discardLogger(), closeTimeout)
	})

	return newPlayRoute(inst.plane, inst.store, inst.plugins, discardLogger())
}

// TestThePlayRouteSystemsSeamActuallyAnswers is the assertion the nil check cannot make.
//
// **A seam wired to a function that returns `nil, nil` is a nil check's blind spot**, and
// it is a more plausible mistake than leaving the field unset: the field is populated,
// the route is mounted, every struct-shaped check passes, and the die sheet renders
// §4.7's empty state for the whole lifetime of the product. Measured — mutating
// `Systems` to `return nil, nil` left `TestThePlayRouteIsWiredWithEverySeam…` green.
//
// So this calls the seam and requires an answer, against a campaign this build
// registers a gameplay system for. The fixture's campaign is registered through the real
// registrar, so the system id in the row is one the resolver genuinely knows — and the
// assertion is that the **closure** reaches it, not that the registry has a dnd5e.
func TestThePlayRouteSystemsSeamActuallyAnswers(t *testing.T) {
	inst := newInstance(t)
	campaign := registerCampaignUnderSystem(t, inst, "play-seam-systems", dnd5e.SystemID)

	handler := wiredPlayHandlerOver(t, inst)

	if handler.Systems == nil {
		t.Fatal("Systems is nil; the nil check has its own test and this one is about " +
			"a seam that is populated but answers nothing")
	}

	system, err := handler.Systems(t.Context(), campaign.ID)
	if err != nil {
		t.Fatalf("Systems(%d) error = %v, want nil. A campaign this build registers a "+
			"system for must answer", campaign.ID, err)
	}

	if system == nil {
		t.Fatalf("Systems(%d) returned a nil system and no error. The die sheet "+
			"renders §4.7's empty state for a nil system and for an error alike, so "+
			"this is indistinguishable in the document from the seam being unwired — "+
			"which is why it needs an assertion that calls it", campaign.ID)
	}

	if system.ID() != dnd5e.SystemID {
		t.Errorf("Systems(%d) returned the system %q, want %q — the seam must resolve "+
			"the campaign's own `system_id` through the registry, not answer for the "+
			"build in general", campaign.ID, system.ID(), dnd5e.SystemID)
	}
}

// TestThePlayRouteSnapshotSeamReadsTheLiveState is the same argument for the other
// function seam, and the "populated but answers nothing" case is different here: a
// `Snapshot` that reports `false` for every campaign renders an empty token list on
// every first paint, which is exactly the moment the document has something to say.
//
// The state is not opened here — a hub opens states, and this route does not — so the
// assertion is the honest one for an unopened campaign: `false`, no document, no panic.
// Asserting a *populated* document would require a resolver and a mutation, which is
// the resolver's own suite's claim and not this file's.
func TestThePlayRouteSnapshotSeamReadsTheLiveState(t *testing.T) {
	inst := newInstance(t)
	campaign := registerCampaignUnderSystem(t, inst, "play-seam-snapshot", dnd5e.SystemID)

	handler := wiredPlayHandlerOver(t, inst)

	if handler.Snapshot == nil {
		t.Fatal("Snapshot is nil, so the first server-rendered document never shows " +
			"placements — which is the render before the socket delivers the snapshot, " +
			"and therefore the only render where the server has anything to say")
	}

	// Both halves, because `false` and an **empty** document are two different claims.
	// Asserting only `live == false` left a seam that answered `false` with a fabricated
	// placement list green, and a caller that reads the document without checking the
	// flag would render that ghost on the table.
	document, live := handler.Snapshot(t.Context(), campaign.ID)
	if live {
		t.Errorf("Snapshot(%d) reported a live state for a campaign no client has "+
			"joined. A hub opens states, so `false` is the truth here and `true` would "+
			"mean the seam invented one", campaign.ID)
	}

	if len(document.Placements) != 0 || document.Revision != 0 || document.Paused {
		t.Errorf("Snapshot(%d) answered false — no state is live — but returned %+v "+
			"with placements, a revision or a pause flag. False means nothing to "+
			"show; data alongside it is a document for a table that has none",
			campaign.ID, document)
	}

	// A campaign id that exists in no state at all, because "no state" and "a state
	// for a different campaign" must be the same answer.
	if _, live := handler.Snapshot(t.Context(), campaign.ID+9999); live {
		t.Error("Snapshot reported a live state for a campaign id that cannot exist. " +
			"The registry is keyed by id, so this would mean it answered for the " +
			"wrong campaign")
	}

	// **And the case the `false` assertions above cannot reach: a state that IS
	// live.** Asserting only "no state" is a check the seam passes by returning
	// `realtime.Document{}, false` forever, which is a token list that is empty on
	// every first paint — measured: mutating the seam to ignore the registry left this
	// test green, and so did dropping `Revision` and `Paused` from the document.
	//
	// So the state is opened for real, through the hub, and the document is required to
	// answer for *that* campaign and to carry the revision and pause flag the state
	// holds. `Join` is the product's own door: a hub is the only thing that causes a
	// state to be opened, which is `realtime.HubConfig.States`'s own comment.
	peer, err := inst.plane.hub.Join(t.Context(), campaign.ID, realtime.Viewer{
		ID:   realtime.UserID(inst.owner.ID),
		Role: domain.RoleGM,
	})
	if err != nil {
		t.Fatalf("hub.Join(%d) error = %v, want nil. Without a live state the rest of "+
			"this test asserts nothing", campaign.ID, err)
	}

	t.Cleanup(func() { peer.Leave() })

	// **The state is moved first, and that is the whole point of this block.**
	//
	// A state that has just been opened sits at revision 0 with nothing paused and no
	// placements, so comparing the document against it proves nothing: every field
	// matches whatever the document happens to carry. Measured — mutating the seam to
	// drop `Revision` and `Paused` from the document it returns left that comparison
	// green, because a stripped document and a fresh state agree on every field a
	// fresh state has.
	//
	// `Create` and `SetPaused` are exported on `CampaignState`, so the state can be
	// moved here without a resolver, an actor, or an intent frame — and with them every
	// field the document claims becomes a value other than the default. This is the
	// rule the repository states for a11y audits and for the demo gate: a test must
	// assert on a state the mutation would actually change.
	state, held := inst.plane.registry.Get(campaign.ID)
	if !held {
		t.Fatalf("the registry does not hold a state for campaign %d after the hub "+
			"opened one, so the assertions below have nothing to compare against",
			campaign.ID)
	}

	if _, err := state.Create("a-token", realtime.Placement{ID: "a-token"}); err != nil {
		t.Fatalf("CampaignState.Create() error = %v, want nil. A placement is what "+
			"makes this state's document differ from an empty one", err)
	}

	if _, err := state.SetPaused(true); err != nil {
		t.Fatalf("CampaignState.SetPaused() error = %v, want nil", err)
	}

	document, liveAfter := handler.Snapshot(t.Context(), campaign.ID)
	if !liveAfter {
		t.Fatalf("Snapshot(%d) reported no state after the hub opened one. The seam "+
			"is not reading the registry the hub resolves against", campaign.ID)
	}

	if document.Revision != state.Revision() {
		t.Errorf("Snapshot reported revision %d, want the state's %d. A first render "+
			"whose revision disagrees with the authority is a client that opens a "+
			"socket and is handed a document it must discard",
			document.Revision, state.Revision())
	}

	if document.Paused != state.Paused() {
		t.Errorf("Snapshot reported paused=%v, want the state's %v", document.Paused,
			state.Paused())
	}

	if !document.Paused {
		t.Error("the state is paused and the document is not. The pause flag is what " +
			"stops a client accepting its own rolls, so a document that drops it " +
			"describes a table that is still accepting them")
	}

	placed := make(map[string]bool, len(document.Placements))

	for _, placement := range document.Placements {
		placed[string(placement.ID)] = true
	}

	if !placed["a-token"] {
		t.Errorf("Snapshot's document carries %d placement(s) and none is the one "+
			"just created. This is the token list's only server-side source, so an "+
			"empty one here is an empty token list on every first paint",
			len(document.Placements))
	}

	// And the other campaign's state must not be this one's. Two campaigns open at
	// once is the case where a seam reading the wrong key is invisible to every
	// assertion above.
}

// The cross-campaign case — two states open at once, and the seam answering for the
// wrong one — is deliberately **not** asserted here.
//
// Two freshly opened states both sit at revision 0 with no placements, so their
// documents compare equal and equality proves nothing; separating them needs a
// mutation through the resolver, and `realtime.Actor` carries an unexported campaign
// field, so the only way to build one from outside the package is a path this test does
// not have. Writing the assertion anyway would produce a check that cannot fail.
//
// It belongs with the resolver's own suite, which already mutates states and has the
// actor. Recorded here rather than written, because a test that cannot fail is worse
// than no test and this one would have looked like coverage.

// membershipLister is the shape the wired `Campaigns` must have.
//
// **A declared type, not an anonymous `var`.** `any(x).(someLocalVar)` does not
// compile: an assertion needs a *type*, and a variable holding an interface type is
// a value. Writing it as `var scoped interface{…}` reads correctly and fails to build,
// which is worth recording because it is the kind of thing that gets "fixed" by
// switching to a string comparison.
type membershipLister interface {
	CampaignsForUser(ctx context.Context, userID int64) ([]domain.Campaign, error)
}

// sortedNames is the seam set in a stable order, so the `t.Logf` above is readable and
// two runs produce the same line. Go prints maps in sorted key order for `fmt` already,
// but a test log that depends on the formatter is a test log that changes when the
// formatter does.
func sortedNames(set map[string]struct{}) []string {
	names := make([]string, 0, len(set))

	for name := range set {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

var _ = context.Background
