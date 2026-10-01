---
title: "0034 — TV mode is a mode, not a width band"
description: "A television and a tablet in landscape are indistinguishable to CSS, so the distinction is a resolved root attribute rather than a breakpoint, and it lives in its own stylesheet."
lede: "Every signal a media query can read says 'large desktop' about a 55-inch television and about a 12.9-inch tablet in landscape. They need opposite layouts, so the question is not which CSS feature to use but where the signal comes from at all."
weight: 224
date: "2026-10-02"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

UI §3.3's opening is the reason this is a decision rather than an implementation detail:

> A 55" television and a 12.9" tablet in landscape are **indistinguishable to CSS**: coarse pointer,
> never hover, wider than 1280px.

Both are a wide viewport, both report a coarse pointer, and neither ever fires `:hover`. Set a 12.9"
tablet down next to a television at 1440 CSS px and every media feature reports the same device class.
They need *opposite* layouts — 10-foot type read from three metres, against 2-foot type read from
thirty centimetres — so a rule keyed on viewport geometry would classify one of them wrongly no matter
which geometry feature it used.

The instinct is to reach for `(min-width: 137.5rem)` or `(pointer: coarse)`, because both are the
tools available and both "work" on the obvious test case. The obvious test case is the reason they do
not work: the tablet and the television are the same width and the same pointer, so a rule that
distinguishes them is not measuring anything that differs between them.

Three sources of signal remain, and only one of them is reliable:

1. An explicit user choice — a URL parameter, then a cookie.
2. A user-agent hint, used only before a cookie exists.
3. Nothing, which means a laptop layout.

Signals 1 and 2 do not survive into CSS. They are resolved *before the first paint* by an inline
script in `<head>`, which writes two attributes onto the document element. Everything downstream —
every tier, both themes, the entire responsive system — reads those two attributes. That is UI §3.7,
already accepted, and this decision is about what follows from it: TV is one of the values of
`data-ui`, and therefore a mode in exactly the sense the resolver makes it one.

## Decision

**TV is a `[data-ui="tv"]` mode. It is never a width band, and no rule that selects TV mode is inside
a `@media` condition that describes geometry or input capability.**

The decision has four parts.

**The mode lives in its own stylesheet**, `internal/web/static/css/tv.css`, rather than as a section
of `shell.css`. `shell.css` mixes mode rules and width rules — a `:root[data-ui="tv"]` block sits
beside a `@media (min-width: 80rem)` block — and a file that is one or the other can be read as one.
The file exists so that "is any part of this selected by geometry?" has the answer *no, and you can
check in one glance*, and so that a rule added to the mode cannot accidentally inherit a width
condition.

**The grid areas stay in `shell.css`.** §3.4's block —

```css
.shell {
  grid-template-areas: "header header" "main rail" "footer footer";
  grid-template-rows: 5rem minmax(0, 1fr) 3rem;
  grid-template-columns: minmax(0, 1fr) 26rem;
}
```

— is stated there, with the lengths coming from the declared constants
(`--header-h-tv`, `--footer-h-tv`, `--rail-right-tv`) rather than restated. Duplicating the areas in a
second file would be two copies of one map with nothing holding them together, which is the same class
of bug as a breakpoint written as a literal in three media queries with one declaration —
`TestEveryMediaQueryMatchesADeclaredConstant` exists for that, and §3.7's "one canonical DOM, tiers as
CSS variants, so tiers cannot drift" is the rule a second copy would break. What `tv.css` holds is
everything the grid has no other reason to know about: the horizontal navigation strip that replaces
the left rail, the D-pad focus rule, and the 10-foot typographic treatment.

**The accepted failure mode is not fixed.** An unrecognised television browser shows the laptop layout
until someone presses the mode switcher once, and the choice then persists. §3.3 calls this the
correct trade against a stale user-agent list and — more to the point — against a tablet
misclassified as a television. The user-agent allowlist stays at its seven tokens. Widening it to
avoid the failure mode converts a one-remote-press inconvenience into a class of misclassification
that no amount of widening removes.

**`prefers-reduced-motion`, `prefers-contrast` and `forced-colors` are honoured in every mode and
never used to infer a form factor.** The three are absent from the banned feature set precisely
because §3.3 requires them to work everywhere; what is banned is a rule in which one of them *chooses
the layout*. A `prefers-contrast: more` rule that widened a rail would be using an accessibility
preference to guess at the display, and a reader who has asked for more contrast because of their eyes
would receive a different layout for it — the opposite of what they asked for.

## Consequences

The distinction between a mode and a width band is now a property of the stylesheet that a test can
fail, not a comment. `TestTheTvModeIsAModeAndNotAWidthBand` in `internal/web/a11y_test.go` resolves
the `@media` nesting around every `[data-ui="tv"]` rule by brace stack and objects to any enclosing
condition naming a geometry or capability feature. Three mutations were used to confirm it can fail:

- the navigation strip wrapped in `@media (min-width: 137.5rem)` — caught;
- a rule conditioned on `(pointer: coarse)` — caught;
- a rule inside `@media (forced-colors: active) and (min-width: 137.5rem)` — caught.

The first version of that test scanned *forward* from the selector and passed all three. It was looking
for a width condition inside a rule's declarations, and the mutation it had to catch puts the condition
*outside*. The scan now resolves nesting rather than searching, which is worth recording because the
failure mode was a gate that looked like it was working.

Television is a mode in every other sense too, and two of those are decisions the record left open:

- **No left navigation.** D-pad traversal of a forty-entry wiki tree is punishing, so the navigation
  becomes a horizontal strip of five destinations in the header band and the tree is not in it. Until
  the campaign overview carries the tile grid §3.4 names as the other half, the nested tree is hidden
  at TV; a horizontally scrollable strip is a horizontal scroll trap on a remote.
- **`:focus` is styled identically to `:focus-visible`.** §7.4 requires this, and the reason is not a
  preference: some television browsers do not merely fail to *match* `:focus-visible`, they fail to
  *parse* the selector, so a rule written on it is dropped at parse time and never appears. A rule
  written on `:focus` is universally supported. The cost is a ring on a mouse click on a device whose
  primary input is a remote, which §10.5's D-pad pass records as the trade rather than hides.

The account zone and the header's search field are removed in the mode, and both removals are settled by
the record rather than by taste: §1.3 states that a television "may not sign in, sign out, manage the
instance", and §3.4's strip already contains Search as one of its five destinations. Capability, not
permission — a GM signed in on a television still sees unredacted pages.

## What the sweeps measured, and the two defects they found

The agent-assisted passes (§10.3–§10.7) drove a real browser against a running server. The MCP is
diagnostic only and is not wired into CI; §10.9 requires every *finding* to become a committed Go test,
and the two that mattered did.

**§3.4's last bullet was false in the stylesheet.** The record says "at 3840 CSS px, `--type-scale`
rises to 1.75 and `--rail-right` to 30rem". At 1920 × 1080 the mode measured exactly what the record
wants — a 1504px centre and a 416px rail, which is §3.4's own "centre ≈1504, right rail 26rem" to the
pixel. At 3840 × 2160 the rail measured **416px**, still 26rem, where the record says 30rem.

The cause was that the TV grid read `--rail-right-tv` as a literal in `grid-template-columns`, so
`:root[data-ui="tv-wide"] { --rail-right: … }` was a declaration the grid never saw. Nothing failed:
`--rail-right` was both declared and read *somewhere* — `tv.css`'s navigation strip reads it for its
inline edge — so the coupling test passed over a property that was dead in the one place the record's
sentence is about. A property that is used is not necessarily used *where it matters*. The grid now
reads the indirection property, which is what `:root[data-ui="wide"]` already did, and
`TestTheTvRailWidensAtTvWide` asserts the chain link by link.

**The inherited type size was the browser's default, not the design's.** An element with no explicit
`font-size` inherited 16px from the user agent rather than §5.1's `--text-base` — measured at 390px as
16px on a skip link while every sized control was 17px. Small, and structurally worse: an element added
later without a type token would render at the browser's default and nothing would say so.
`body { font-size: var(--text-base) }` fixes it, and the root stays at `100%` so the reader's browser
zoom remains the only multiplier, which is what 1.4.4 is about. Now measured at 17px at `large` and
18.06px at `compact` — §5.1's two rows.

Three findings could not be automated, and are recorded here rather than dropped:

- **The compact bottom bar is never exercised by a sweep.** No campaign route exists in this phase, so
  every route fixture is pre-campaign and the bar renders nothing. Its CSS is covered by the chrome
  package's own composition tests and by the `:root:not([data-ui])` no-script rule, but no browser pass
  can see it until P6 registers a campaign. This is the honest gap.
- **The navigation strip in TV mode is unverified.** `chrome.CampaignNav` reaches no route in this
  phase, so the strip's horizontal layout, its five destinations and its collapsed state were never
  rendered. The CSS is in place and the component's own tests cover the markup; the *layout* awaits a
  campaign route.
- **`/favicon.ico` answers 404**, which is the only console error on any route. Cosmetic, and not an
  accessibility finding — recorded so the next person does not read it as a regression.

The cost is that the mode's rules are duplicated in one file and the grid's in another, so a reader
looking for "what does TV mode do" has to open `tv.css` and then `shell.css`. The header of
`tv.css` says so explicitly and states why the areas are not repeated.

## Alternatives considered

**A `@media (min-width: 137.5rem)` band, gated on `pointer: coarse`.** The obvious implementation, and
the one a reader of the CSS would expect. Rejected on §3.3's own argument: it classifies a 12.9" tablet
in landscape as a television, which is the specific outcome the record refuses to accept in exchange
for the failure mode it fixes. The two signals are not independent — every device that trips the
first also trips the second.

**Widening the user-agent allowlist to catch more televisions.** Rejected because the list cannot be
completed. Television browser names churn faster than a release cycle, and the cost of a false negative
is one remote press. The cost of a false positive is a desktop interface with 56-pixel targets and no
hover, read from thirty centimetres — which is the "another item is still on the table" experience
§3.3 is buying the narrow list to avoid.

**Detecting the television from `devicePixelRatio` or screen dimensions.** Rejected: a 4K television
reporting DPR 2 sees a 1920 × 1080 CSS viewport and is indistinguishable from a desktop, which is
precisely why `--breakpoint-tv-wide` is 137.5rem rather than 3840. The signal does not exist.

**Inferring the mode from `prefers-contrast: more`, `forced-colors` or reduced motion.** Rejected, and
banned by the same test. Each is a statement about a reader's needs or their display's capabilities,
and using one to choose a layout means a reader who set an accessibility preference receives a different
interface as a side effect. §3.3 requires all three to be honoured *in* every mode, which is the
opposite relationship.

**Leaving TV as a section of `shell.css`.** Rejected because the file would then be neither a mode's
stylesheet nor a width-tier stylesheet, and "does any rule here select on geometry?" — the one question
that matters for this decision — would need a brace-stack scan across a file that also contains the
height-tier rules, short-landscape handling and the forced-colours block. Splitting it makes the answer
visible without running anything.