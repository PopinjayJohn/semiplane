package e2e_test

// Claim three: a reader with no account reads the public campaign, and gets 404 —
// **not 403** — for a private one.
//
// # Why 404 rather than 403 is the whole claim
//
// AGENTS.md's security invariants: "**No access is 404, never 403.** A private
// campaign and a campaign that does not exist answer identically — same status, same
// body — or a 403 becomes an existence oracle." ADR 0024 is the record.
//
// So this file does not assert "not 403". It asserts something strictly stronger and
// which a 403 cannot pass:
//
//   - the refusal is a **404**;
//   - its body is **byte-identical** to the body a campaign that was never
//     registered answers with, fetched from the **same client** at the **same
//     moment**; and
//   - the same client receives **200** for the public campaign, so the difference is
//     the campaign's visibility and not anything about the request.
//
// The third is the control that makes the second mean anything, and the second is
// the control that makes the first mean anything. A suite asserting only "the private
// campaign is not 200" is satisfied by a route that 404s everything, which is the
// defect `router.go` documents at length: 404-for-everything is indistinguishable
// from the correct answer while no campaign route is registered.
//
// # Every page, not one
//
// Both halves are sweeps over **every page of the vault copy**, not over one
// hand-picked index. A campaign whose front page is refused and whose other pages
// are not is a confinement defect, and a sweep over one URL would not see it. The
// public half additionally checks that each page answers with **its own** content, so
// a 200 that returned the same document for every path would not pass.
//
// # What is *not* claimed here
//
// The 404 body is `application/json`, not a rendered document, and that is correct:
// a refusal is a sentence from `campaigns.writeError`, and the reader who typed the
// URL is the only one who will ever see it. There is no §10.2 audit over it and none
// is claimed; see `play.refuse`'s comment for why a shell document at a refusal URL
// would be a second rendered document with no audit.

import (
	"bytes"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// TestEveryPageOfThePublicCampaignAnswersAReaderWithNoAccount is claim three's first
// half, over every page of `public-post`.
//
// **Three assertions per page:**
//
//  1. the status is **200** for a client holding no session at all;
//  2. the served document is **that page** — its `<h1>` is its own name and its body
//     carries its own first heading — so a 200 that returned one document for every
//     path would not pass; and
//  3. the campaign navigation the server rendered **is present**, which is UI §4.6's
//     campaign variant. A reader inside a campaign is inside it whether or not they
//     have an account, and a shell that rendered the pre-campaign variant here would
//     put a campaign page in a document that says there is none.
//
// **And the absence of the Game Master's affordances**, because §4.3's rule is
// "absent, not disabled": an anonymous reader of a public campaign must be shown no
// Admin destination, no sign-out form and no account name. A disabled control is
// still a control, and a shell that rendered one greyed out would be telling a
// stranger this instance has a Game Master.
func TestEveryPageOfThePublicCampaignAnswersAReaderWithNoAccount(t *testing.T) {
	boot := sharedDemo(t)

	pages := pagesOfCampaign(t, boot, publicSlug)
	if len(pages) == 0 {
		t.Fatalf("the public campaign holds no pages in the vault copy, so an "+
			"anonymous read would pass on a campaign that demonstrates nothing. The "+
			"artefact declares %s as public precisely so a stranger can open it", publicSlug)
	}

	for _, page := range pages {
		t.Run(page.name(), func(t *testing.T) {
			document := boot.requireDocument(t, boot.anon, boot.base+page.url())

			heading := demoRequireTestID(t, document, "page-title")
			if got := demoFold(strings.TrimSpace(demoText(heading))); got != page.name() {
				t.Errorf("the anonymous reader's document heads %q, want %q. So the "+
					"route answered 200 with something that is not this page",
					got, page.name())
			}

			if page.heading != "" && !slices.Contains(demoHeadings(
				demoRequireTestID(t, document, "page-body"),
			), demoFold(page.heading)) {
				t.Errorf("the anonymous reader's document for %s carries no %q heading, "+
					"so the 200 did not carry this page's body", page.url(), page.heading)
			}

			// §4.6: a reader inside a campaign is inside it.
			if demoByTestID(document.root, "shell-nav") == nil {
				t.Errorf("the anonymous reader's document for %s has no campaign "+
					"navigation. UI §4.6's campaign variant is chosen by being inside a "+
					"campaign, not by having an account", page.url())
			}

			// §4.3: absent, not disabled.
			for _, adminRow := range demoElementsWithAttr(document.root, "data-testid", "nav-admin") {
				t.Errorf("the anonymous reader's document for %s offers the Admin "+
					"destination at %s. §4.3 wants the Game Master surface *absent* "+
					"for everybody else, and a control that is merely greyed out is a "+
					"control", page.url(), adminRow.Data)
			}

			for _, form := range demoElementsWithAttr(document.root, "data-testid", "shell-sign-out") {
				t.Errorf("the anonymous reader's document for %s carries a sign-out "+
					"form at %s. There is no session to sign out of", page.url(), form.Data)
			}

			if name := demoByTestID(document.root, "header-account"); name != nil {
				if text := strings.TrimSpace(demoText(name)); text != "" {
					t.Errorf("the anonymous reader's document for %s names an account "+
						"(%q). This request carried no session", page.url(), text)
				}
			}
		})
	}
}

// TestEveryPageOfAPrivateCampaignAnswersAReaderWithNoAccountAsACampaignThatDoesNotExist
// IsTheSame404 is claim three's second half.
//
// **Three assertions per page, and the second is the one that matters:**
//
//  1. the status is **404** — never 403, never 401, never a redirect;
//  2. the body is **byte-identical** to the body the same client gets for a slug no
//     seed ever registered; and
//  3. `Cache-Control` is **`private, no-store`**, because every gate response in this
//     project is reader-dependent — the same URL answers 404 to a stranger and 200 to
//     the Game Master — and a reverse proxy in front of a self-hosted instance is
//     the ordinary deployment.
//
// **Both private campaigns are swept**, so the claim is about visibility rather than
// about one slug: `greyhaven` and `forgotten-realm` are both `private` and both must
// be invisible. The nonexistent slug is `no-such-campaign`, which the artefact's
// manifest never declares and which `domain.ValidateSlug` would accept if it were
// registered.
func TestEveryPageOfAPrivateCampaignAnswersAReaderWithNoAccountAsACampaignThatDoesNotExist(
	t *testing.T,
) {
	boot := sharedDemo(t)

	absent := boot.base + "/c/no-such-campaign/wiki/index"

	absentStatus, absentBody := demoGet(t, boot.anon, absent)
	if absentStatus != http.StatusNotFound {
		t.Fatalf("GET %s as a reader with no account = %d, want 404. This is the "+
			"answer the private campaigns' refusals are compared against, and if it "+
			"is not 404 there is nothing to compare", absent, absentStatus)
	}

	for _, campaign := range []string{showcaseSlug, degradedSlug} {
		for _, page := range pagesOfCampaign(t, boot, campaign) {
			t.Run(page.name(), func(t *testing.T) {
				url := boot.base + page.url()

				status, body := demoGet(t, boot.anon, url)

				if status != http.StatusNotFound {
					t.Errorf("GET %s as a reader with no account = %d, want 404. S-8 "+
						"answers no access with a status that does not say why: a 403 "+
						"confirms the campaign exists and is a one-bit existence oracle "+
						"any stranger can read off a URL", url, status)
				}

				if !bytes.Equal(body, absentBody) {
					t.Errorf("GET %s answers %q and GET %s answers %q. They must be "+
						"byte-identical: the difference between a private campaign and "+
						"a campaign that was never registered is nothing a reader may "+
						"observe, and a body that varies with the slug is the oracle "+
						"S-8 rules out", url, string(body), absent, string(absentBody))
				}

				if got := demoHeaders(t, boot.anon, url).Get("Cache-Control"); got !=
					"private, no-store" {
					t.Errorf("GET %s sends `Cache-Control: %q`, want %q. Every gate "+
						"response in this project is reader-dependent -- the same URL "+
						"answers 404 here and 200 to the Game Master -- and a reverse "+
						"proxy in front of a self-hosted instance is the ordinary "+
						"deployment", url, got, "private, no-store")
				}
			})
		}
	}
}

// TestTheGameMasterReadsWhatTheStrangerCannot is the control for the test above, and
// it is a separate test rather than a line inside it.
//
// **Without it, "the private campaign is 404" is satisfied by a route that 404s
// everything.** With it, the same URLs are 200 for `demo-gm` — so the refusal is an
// access decision, the campaign exists, and its pages are in the index.
//
// Both private campaigns and every page of each, for the same reason the refusals
// are swept.
func TestTheGameMasterReadsWhatTheStrangerCannot(t *testing.T) {
	boot := sharedDemo(t)

	for _, campaign := range []string{showcaseSlug, degradedSlug} {
		for _, page := range pagesOfCampaign(t, boot, campaign) {
			t.Run(campaign+"/"+page.name(), func(t *testing.T) {
				document := boot.requireDocument(t, boot.gm, boot.base+page.url())

				heading := demoRequireTestID(t, document, "page-title")
				if got := demoFold(strings.TrimSpace(demoText(heading))); got != page.name() {
					t.Errorf("%s's document heads %q, want %q", demoGM, got, page.name())
				}
			})
		}
	}
}

// pagesOfCampaign is one campaign's pages of the vault copy, in path order.
//
// **The campaign is one of the artefact's, and the pages come from the copy the
// server serves** — so the sweep cannot pass on pages the instance does not have, and
// cannot miss one it does.
func pagesOfCampaign(t *testing.T, boot *demoBoot, campaign string) []demoPage {
	t.Helper()

	pages := walkDemoVault(t, boot.vault, demoKindsForBuild(t))

	var found []demoPage

	for _, page := range pages {
		if page.campaign == campaign {
			found = append(found, page)
		}
	}

	return found
}
