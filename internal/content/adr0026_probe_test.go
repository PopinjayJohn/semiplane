package content_test

// Whether ADR 0026's reading is the reading.
//
// # The claim being checked
//
// ADR 0026, `AGENTS.md` and `splitQualifier`'s own doc comment all say the same thing:
//
//   The **leading slash** is what makes the relative reading and the cross-campaign
//   reading mutually exclusive. `[[/campaign/Page]]` is another campaign;
//   `[[Other/Foo]]` is a *relative* link inside the home campaign.
//
// The boundary work item for phase 11 reported a measurement that contradicts it: a
// reference written **without** the slash, naming the *home* campaign's own slug,
// still resolves. If that is right then the slash is not what decides, and two
// documents in the product assert a rule the code does not implement.
//
// # Why this is an internal test rather than one more case in `links_test.go`
//
// It is a claim about an **ADR**, not about a page. `links_test.go` is organised by
// link shape; this is organised by "which record is true", and putting it in the
// middle of a suite organised the other way would hide the question it exists to ask.
// It also needs to state the answer either way — a test that only asserts the
// current behaviour is a test that ratifies whatever the code happens to do, which is
// the failure mode this repository keeps meeting.
//
// # What the answer is, and why
//
// **The relative reading wins, and the slash is what makes it unambiguous rather than
// what enables it.** `Resolver.resolve` joins a slashless reference against the
// *writing page's* directory and looks it up; only when that fails does `recordOutside`
// get a chance, and `splitQualifier` refuses anything whose `vaultRoot` is false — so
// a slashless `[[public-post/town-notice]]` from inside `public-post` resolves as
// `public-post/public-post/town-notice`… or as `town-notice` from a page at the top.
//
// The readings are **not** mutually exclusive, and `recordOutside`'s step 3 is why.
// A reference that fails the relative reading is re-tried as a **bare name**:
//
//	name := ref.target
//	if !isName(name) {
//		name = path.Base(name)
//	}
//
// So `[[public-post/town-notice]]` from inside `public-post`, having failed as
// `notes/public-post/town-notice` or `public-post/town-notice`, is looked up as the
// page called `town-notice` — and is found. **The slashless cross-campaign spelling
// works whenever the target's basename is unique in the visible set.** It resolves to
// the *same page* the slashed spelling resolves to, which is why nothing looked broken.
//
// # Why that is worth a record and not a bug fix
//
// The fallback is deliberate — it is what makes a deleted-page report say
// "probably meant" rather than nothing — and it is the same fallback for a bare
// `[[Goblin]]`. Removing it would break ordinary links whose page moved up a
// directory. So the code is right and **the record's claim of exclusivity is the
// defect**: a reader who trusts it will write `[[Other/Foo]]` expecting a relative
// link, get a cross-campaign one, and have no way to tell from the page.
//
// The two are distinguished by which page is found, not by whether one is found — so
// the assertions below hold both readings and check *which* one won.

import (
	"testing"

	"github.com/semiplane/semiplane/internal/content"
)

// TestASlashlessCrossCampaignReferenceResolvesByItsBasename is the finding, stated
// as a test so it cannot regress silently.
//
// **It asserts that the two spellings reach the same page**, which is the surprising
// half and the one a reader of ADR 0026 would not predict.
func TestASlashlessCrossCampaignReferenceResolvesByItsBasename(t *testing.T) {
	t.Parallel()

	home := newCampaign(t, "public-post",
		"index.md", "town-notice.md", "notes/deep.md")

	// The home campaign in the visible set, as the wiki route supplies it.
	visible := []content.VisibleCampaign{home.visible()}

	// **Always from `notes/deep.md`**, which is the whole point: a subdirectory is
	// the only place where the relative reading differs from the top-level one, so a
	// test that also ran from `index.md` would be checking the easy case.
	const from = "notes/deep.md"

	slashed := home.resolveFrom(t, "/public-post/town-notice", visible)
	if !slashed.Resolved {
		t.Fatalf("the slashed spelling did not resolve from %s: %+v", from, slashed)
	}

	bare := home.resolveFrom(t, "public-post/town-notice", visible)

	if !bare.Resolved {
		t.Errorf("the slashless spelling did not resolve from %s: %+v. ADR 0026 says "+
			"it is a relative link naming notes/public-post/town-notice, and it does "+
			"not exist — so the record's claim and the code disagree, and the code is "+
			"what a reader gets", from, bare)
	}

	if bare != slashed {
		t.Errorf("the two spellings disagree:\n slashed  %+v\n slashless %+v\n"+
			"They reach the same page, so the slashless form is not the relative "+
			"link ADR 0026 says it is", slashed, bare)
	}
}

// TestTheBasenameFallbackIsWhatDecidesIt isolates the mechanism, so a future change
// that moves the behaviour has one assertion to break rather than a vague one.
func TestTheBasenameFallbackIsWhatDecidesIt(t *testing.T) {
	t.Parallel()

	home := newCampaign(t, "public-post",
		"index.md", "town-notice.md", "notes/deep.md")

	visible := []content.VisibleCampaign{home.visible()}

	// The *same* slashless spelling, with no page of that basename anywhere in the
	// visible set. Nothing for the fallback to find, so nothing resolves.
	missing := home.resolveFrom(t, "public-post/absent-notice", visible)
	if missing.Resolved {
		t.Errorf("[[public-post/absent-notice]] resolved to %+v. No page of that "+
			"basename exists, so neither reading could have found it", missing)
	}

	// And a basename that **does** exist, reached by a spelling with no path at all —
	// which is the same fallback and the case it was written for.
	bare := home.resolveFrom(t, "town-notice", visible)
	if !bare.Resolved {
		t.Errorf("[[town-notice]] did not resolve from notes/deep.md: %+v", bare)
	}
}

// TestASlashlessReferenceStillLosesToTheRelativeReading is the half that makes the
// fallback safe rather than surprising.
//
// The relative reading is tried **first** and wins whenever it finds anything, so a
// campaign that has its own `notes/public-post/town-notice` gets *that* page from the
// slashless spelling — not the home campaign's `town-notice`. That is the ordering
// which keeps `[[deep/../Goblin]]` meaning one thing, and it is why the fallback can
// be as broad as it is.
func TestASlashlessReferenceStillLosesToTheRelativeReading(t *testing.T) {
	t.Parallel()

	home := newCampaign(t, "public-post",
		"index.md", "town-notice.md",
		"notes/deep.md", "notes/public-post/town-notice.md")

	visible := []content.VisibleCampaign{home.visible()}

	got := home.resolveFrom(t, "public-post/town-notice", visible)
	if !got.Resolved {
		t.Fatalf("did not resolve: %+v", got)
	}

	if got.TargetPath != "notes/public-post/town-notice" {
		t.Errorf("the slashless spelling resolved to %q, want %q. The relative "+
			"reading is tried first and this campaign has that path, so the "+
			"basename fallback must not have been reached", got.TargetPath,
			"notes/public-post/town-notice")
	}
}

// resolution is the subset of `content.Outbound` these assertions read, named so the
// table above does not carry a nine-field struct through four call sites.
type resolution struct {
	Resolved       bool
	TargetCampaign string
	TargetPath     string
}

// resolveFrom resolves one reference written into one page of the fixture.
//
// **The target is the reference's inside text**, without `[[` and `]]`: `linkRef`
// builds the `content.Reference`, and passing the brackets as well gives the resolver
// a target that begins with `[` — which is why my first version of this file
// reported *nothing resolving at all* and looked like a product defect.
//
// Through `Records`, not `Link`, because `Records` answers "did it resolve and where
// to", which is the question here. `Link` reports an href and a refusal, and a
// cross-campaign refusal is the *correct* outcome to inspect rather than a failure.
//
// The visible set is a parameter rather than `nil` because **the basename fallback
// reads it**, and a nil set would make every cross-campaign spelling unresolved for a
// reason that has nothing to do with the question.
func (f campaign) resolveFrom(
	t *testing.T, reference string, visible []content.VisibleCampaign,
) resolution {
	t.Helper()

	// Every case in this file writes into `notes/deep.md`, which is a constant here
	// rather than a parameter: the linter is right that a parameter nothing varies is
	// a parameter shaped by its first call, and the subdirectory *is* the finding —
	// a top-level writing page resolves `public-post/town-notice` differently.
	const from = "notes/deep.md"

	records := f.resolver(t).Records(
		linkOrigin(t, from), []content.Reference{linkRef(reference)}, visible)
	if len(records) != 1 {
		t.Fatalf("Records(%q) returned %d records, want 1", from, len(records))
	}

	return resolution{
		Resolved:       records[0].Resolved,
		TargetCampaign: records[0].TargetCampaign,
		TargetPath:     records[0].TargetPath,
	}
}
