package live

// The fragment builders: five functions that turn a view model into a `Fragment`
// the stream can decide about and write.
//
// # Why a builder and not a template call at each use
//
// Each of these does three things in a fixed order, and doing them at the call
// site is how any one of them gets left out:
//
//  1. **look the target up by name** — so a selector cannot be invented, and an
//     unknown name is an error while the fragment is being built rather than an
//     empty patch on the wire;
//  2. **render the component** — so the escaping is `templ`'s and there is no
//     string concatenation of untrusted values anywhere near a browser;
//  3. **state the region** — so `Decide` can refuse a fragment whose region is not
//     its target's, and so the refusal happens on the server rather than in a
//     reviewer's head.
//
// # Why the context is the caller's, minus its cancellation
//
// `internal/httpapi/events`' `renderNotice` explains this at length for the
// editor's notice, and the same argument applies to every one of these: a stream
// outlives the handler budget, `templ`'s generated `Render` checks `ctx.Err()`
// before every element, and a render using the request's context would succeed for
// the first twenty-five seconds of a stream and fail on every patch after that —
// a sidebar that goes permanently silent, logging one error per change, with no
// symptom a reader can describe. `context.WithoutCancel` keeps the request's
// *values* and drops only the deadline.

import (
	"context"
	"fmt"
	"strings"

	"github.com/a-h/templ"
)

// render is the one rendering path, shared by the five builders.
//
// `strings.Builder` rather than `bytes.Buffer` because templ's `Render` takes an
// `io.Writer` and `strings.Builder` implements it without an allocation per
// write — which is the whole of the difference at one fragment a second, and the
// reason is not worth a paragraph.
func render(ctx context.Context, component templ.Component) (string, error) {
	var out strings.Builder

	if err := component.Render(ctx, &out); err != nil {
		return "", fmt.Errorf("render the component: %w", err)
	}

	return out.String(), nil
}

// build renders one fragment and states its target and region.
//
// The two lookups are the failure modes worth naming separately: a target that is
// not declared is a programming error and carries the declared list in its
// message, and a component that will not render is a wiring fault reported
// through the same return. Neither is reachable from a correct caller, and both
// are refused rather than papered over with an empty fragment — an empty fragment
// patched into a region is a notice that patches nothing and says nothing, which
// reads as "nothing is wrong" rather than as a fault.
// The `contextcheck` suppression below is about `templ.Component`, not about a real
// dropped context: a templ component is a `func(context.Context, io.Writer) error` that
// the generated code re-binds with `templ.InitializeContext`, so the context *is*
// threaded into every element. The analyser cannot see through that closure and reports
// that a function holding a `ctx` built a component without passing it — which is what
// every templ call site in this repository does, and which is why the suppression
// carries its reason here rather than appearing at five call sites.
func build(
	ctx context.Context,
	targetName string,
	region Region,
	component templ.Component,
) (Fragment, error) {
	target, err := TargetByNameOrError(targetName)
	if err != nil {
		return Fragment{}, err
	}

	markup, err := render(ctx, component)
	if err != nil {
		return Fragment{}, fmt.Errorf("render the %s fragment: %w", targetName, err)
	}

	return Fragment{Target: target, Region: region, Markup: markup}, nil
}

// RenderInitiative is the initiative tracker's body, for the `initiative` target.
//
// **Region `RegionSilent`**, and it is the only fragment in this package whose
// region is silence. §7.5's announced-content table has no row for "the order
// changed": it has one for "turn change", and that arrives in the polite status
// region. Rendering the order into a live region would make a re-order read the
// whole table's initiative aloud, which is the replay rule with the volume turned
// up.
func RenderInitiative(ctx context.Context, view InitiativeView) (Fragment, error) {
	//nolint:contextcheck // a templ component binds the context itself; see `build`
	return build(ctx, "initiative", RegionSilent, InitiativeFragment(view))
}

// RenderChatLine is one chat line, for the `chat log` target.
//
// `RegionLog` and `ModeAppend`, so the line is announced as itself and the
// container's own state survives — `chat.Panel`'s empty `<ol>` is what the first
// line is appended into, which is the half of §7.5's replay rule that has to be
// true before any line arrives.
func RenderChatLine(ctx context.Context, message ChatLineView) (Fragment, error) {
	//nolint:contextcheck // a templ component binds the context itself; see `build`
	return build(ctx, "chat log", RegionLog, ChatLineFragment(message))
}

// RenderDiceLine is one roll, for the `dice log` target.
func RenderDiceLine(ctx context.Context, roll DiceLineView) (Fragment, error) {
	//nolint:contextcheck // a templ component binds the context itself; see `build`
	return build(ctx, "dice log", RegionLog, DiceLineFragment(roll))
}

// RenderAnnouncement is one polite status sentence, for the `announcement` target.
//
// §7.5's `role="status"` row verbatim, and the reason this is a separate family
// rather than a kind of notice is politeness: the turn change, the hit-point
// change and the applied delta are things a reader wants to hear **between**
// actions, and the degraded conditions are things they want to hear **now**.
func RenderAnnouncement(ctx context.Context, view AnnouncementView) (Fragment, error) {
	//nolint:contextcheck // a templ component binds the context itself; see `build`
	return build(ctx, "announcement", RegionStatus, AnnouncementFragment(view))
}

// RenderNotice is one degraded condition, for the `notice` target.
//
// `RegionAlert`, for the three conditions `NoticeKinds` names and no others. The
// close of the list matters as much as the entries: a fourth condition added here
// would be announced with the urgency of a lost connection, and a reader who has
// learned that this region means *now* stops reading it.
func RenderNotice(ctx context.Context, view NoticeView) (Fragment, error) {
	//nolint:contextcheck // a templ component binds the context itself; see `build`
	return build(ctx, "notice", RegionAlert, NoticeFragment(view))
}
