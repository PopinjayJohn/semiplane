package edit

// The editor's disclosure surface: which `[!secret]` callouts this page has, and the
// control that reveals each.
//
// # Why this is a separate file from `edit.go`
//
// `edit.go` serves the editor. This decides what the *disclosure affordance* on that
// editor says, and the two have different owners, different failure directions and a
// security property between them:
//
//   - The editor's job is to save a document. Its danger is losing one — a silent
//     overwrite, which S-6.3 forbids.
//   - This surface's job is to **publish** something. Its danger is publishing
//     something nobody read, which is not an S-6 failure at all.
//
// So the mapping from a page's source to a list of controls lives here, where the
// rule "a control may only name a resolved anchor" is stated once, rather than in the
// middle of a handler that also computes an `ETag`.
//
// # What crosses into the view, and what does not
//
// **The anchor, the title, the position and the marker byte. Never the body.**
//
// The body is the secret. §5.6.1 is that the secret body is absent from a non-GM's
// response, and a view model that carried it would put it in a struct that could be
// logged, formatted, compared, or rendered into a template that did not think about
// it — which is the shape `components/live` refuses for `Message` and
// `wiki.WikiPageView` refuses for `Preview`. The control needs to say *which* secret
// and *which way*, and that is four values.
//
// # The endpoint is derived here, not configured
//
// The reveal request goes to `/c/{slug}/secrets/{path…}`, which is the route S6
// mounted. It is built from the campaign and the page rather than handed in, because
// a caller that could put a different endpoint on the control could point a GM's
// disclosure at somewhere else — and `Endpoint` is a string in a view model, which is
// exactly the kind of value a template should not be able to invent.

import (
	"net/url"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/web/components/secret"
)

// disclosuresFor builds the editor's disclosure surface for one page.
//
// # `MarkerShown`, and why the editor is the page that shows it
//
// Plan item D15 asks whether a revealed callout carries a "Revealed" marker on the
// published page, and §4.10.4 answers it as a per-campaign setting. **The editor shows
// it unconditionally and the published page does not**, and the reason is not a
// preference: the editor is a GM's own working surface where a callout that reads
// `+` and renders as public needs to say so, because the GM is about to decide
// whether that is correct. The published page has no such decision — by the time it
// is published the answer is already in the byte.
//
// So this passes `MarkerShown` and the wiki route passes nothing, and the zero value
// (`MarkerOff`) is the published page's answer. A caller that forgets the argument
// therefore gets the safe one, which is the direction the forgetting can err in.
func (h *Handler) disclosuresFor(
	campaign domain.Campaign, path, source, contentHash string,
) secret.DisclosuresView {
	view := secret.DisclosuresView{
		Endpoint:  revealEndpoint(campaign.Slug, path),
		Page:      path,
		Validator: validator(campaign.ID, path, contentHash),
		Marker:    secret.MarkerShown,
	}

	// **Scanned from the bytes in hand, not from the index.** The index's
	// `content_hash` is what the validator above is derived from, and the callouts
	// have to come from the same read — a control list built from one read and a
	// validator from another is a reveal that presents a precondition for a
	// different document than the one it names.
	for _, found := range content.ScanSecrets(source) {
		view.Callouts = append(view.Callouts, secret.CalloutView{
			// **The resolved anchor, computed here.** `secrets.resolve` matches a
			// requested anchor against exactly this derivation, so a control that
			// carried something else would name a secret the endpoint cannot find —
			// and a control the endpoint cannot resolve is a reveal button that
			// 404s, which a GM reads as "this secret cannot be revealed".
			Anchor:  content.Resolve(campaign.ID, path, found).Value,
			Ordinal: found.Ordinal,
			Title:   found.Title,
			State:   stateOf(found.State),
		})
	}

	return view
}

// stateOf maps `content`'s marker byte onto the UI tier's own vocabulary.
//
// **A mapping rather than a shared type**, and the reason is the same one that put a
// local `LedgerRow` in `internal/content/anchor.go`: `content` is below the web tier
// and cannot import it, and `secret.State` is a presentation concern that belongs
// beside the components that render it. Two vocabularies for one byte, joined in one
// function, with the mapping's failure direction named — see below.
func stateOf(state content.SecretState) secret.State {
	if state.IsRevealed() {
		return secret.StateRevealed
	}

	return secret.StateHidden
}

// revealEndpoint builds the reveal route for one page.
//
// # Escaped, and every segment
//
// `url.PathEscape` on the page path is not optional. The path is attacker-reachable
// in the sense that matters here: it comes from a URL the GM's browser holds, and a
// page whose name contains a `/` or a space would otherwise produce a path that
// addresses a *different* resource — or none. §S-3.5's rule is that a front-matter
// value can be a path, and this is the same class of value reached from the other
// direction.
//
// The campaign slug is escaped for the same reason and is not otherwise constrained
// beyond `[a-z0-9-]`, so escaping it costs nothing and removes a question.
func revealEndpoint(slug, path string) string {
	return "/c/" + url.PathEscape(slug) + "/secrets/" + url.PathEscape(path)
}
