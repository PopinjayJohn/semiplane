---
title: "0033 — Two conflicts in the UI record, resolved and recorded"
description: "Where §4.3 and §6.6 disagree about persisting the collapsed navigation, and where §4.1's switchers meet §3.7's canonical DOM, the later and more specific rule governs — and both outcomes are recorded rather than picked in markup."
lede: "Two deviations from an immutable design record, both raised during phase 5 rather than resolved in a template. A record that is wrong is corrected by a decision record and a staleness note, never by an edit, and never by silently choosing."
weight: 223
date: "2026-10-02"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

The responsive UI design record is
[immutable]({{ "contributing/spec/" | relURL }}#the-design-records-are-immutable): where it is wrong,
the correction is a decision record plus an entry in the design index's "Known staleness". Two places in
it are wrong in ways that a template has to resolve one way or another, and both were escalated during
phase 5 rather than decided in markup.

They are recorded here together because they have the same shape and the same answer. Both are cases
where the record states two things that cannot both hold, and in both the resolution follows from one
rule: **where two sections disagree, the later and more specific one governs**, and the losing section's
*intent* is preserved by other means. Applying "last writer wins" to a design record is not a principle
— it is a heuristic, and it is only defensible where one section genuinely refines the other.

## Decision

**Conflict one: §4.3 persists the collapsed navigation in `sp_ui`; §6.6 fixes `sp_ui` to two fields and
rules out the rest. §6.6 governs. The collapse is a `data-` attribute.**

§4.3 says "User may collapse to a 3.5rem icon rail, persisted in `sp_ui`". §6.6 says `sp_ui` carries
`theme` and `ui` and nothing else, and gives the reason in its own words: **"No density field — density
was not selected, and a dangling token is worse than no token."**

§6.6 is later in the document, it is more specific — it is the section that defines the cookie's
schema — and it states a rule about fields in general rather than about one field. It also explains its
own reasoning in a way that applies verbatim: a field nobody reads is a field that can be set to a value
nothing honours, and a reader who has set it has no way to find out why nothing happened.

So `sp_ui` stays at two fields and the collapse is `[data-nav="collapsed"]`, an attribute the client
sets on the `<nav>` and the stylesheet reads. **The cost is recorded rather than argued away:** the
collapse resets to expanded on a new device. That is a preference, not data, and it is the right thing
to lose — a reader who collapses the rail on a laptop and finds it expanded on the same laptop the next
day has a real complaint, and a reader who does not notice is not damaged by it.

What survives from §4.3 is the part that is about the *interface* rather than the transport, and all of
it is kept: the 3.5rem icon rail, and the rule that each row keeps its full name so the rail costs no
accessible names. The record's own wording for that is an `aria-label`, and the implementation uses a
text label the stylesheet hides instead — which is stronger, because the accessible name is then the
visible text at every width, satisfying 2.5.3, and there is no second copy of the name in the markup for
the two to disagree about.

**Conflict two: §4.1 puts the campaign switcher in a popup and the mode switcher in a `<select>`; §3.7
requires one canonical DOM across tiers and forbids the document varying by `sp_ui`. §3.7 governs. Both
controls ship in phase 9; what is in the markup now is a link and a hook.**

§4.1 specifies "Campaign switcher is a `<button aria-haspopup="menu">` + `role="menu"` popup on pointer
tiers, a native `<select>` on TV", and a mode switcher as a `<select>` listing auto / laptop / phone /
TV. Both are *client-layer* features: a popup needs a menu to open, and a `<select>` needs a client to
submit from. Rendering them now would mean shipping controls that are focus stops which do nothing —
and the chrome package's one rule about controls is exactly that nothing renders in a state that needs
the client layer to become useful.

The deeper constraint is [0035]({{ "decisions/0035-no-vary-cookie-and-one-blocking-script/" | relURL }}).
The mode switcher's *result* is `sp_ui.ui`, and §3.7 requires the document not to vary by `sp_ui` — so a
`<select>` that submits its value to the server, or that the server reflects back into the markup, makes
the response depend on the cookie. That is the failure
[S-13.5]({{ "contributing/spec/" | relURL }}) rules out, arrived at from the opposite direction from the
one §6.6 anticipated. The switcher must therefore write the cookie *client-side*, exactly as the head
resolver reads it, and the server must never learn the value.

So what ships now is:

- **the campaign's own address, as a link.** Which is also §8.3's rank 1 — the current location — and
  what a 56-pixel bar wants. The popup is phase 9's.
- **the mode switcher and the compact menu button, not rendered.** Both are named in the chrome package
  and in the integration report; neither would be honest to render as a dead control.
- **a declared hook rather than a missing element.** The theme button carries
  `data-chrome="theme"`, the navigation's collapse carries `data-chrome="nav-collapse"`, the disclosures
  carry `data-chrome="disclosure"`, and the rail's trigger is exported as
  `chrome.UtilitiesTrigger("rail")` — because a trigger has to be reachable while the rail is closed, so
  it cannot live inside it, and §4.1 assigns it to none of the banner's zones. The markup for it exists;
  only its placement is deferred.

The theme button is the one §4.1 control that *is* rendered, and it ships in the state that works
without a client: `Theme: auto`, `aria-pressed="false"`. It names the **result**, not the action, which is
§4.1's own rule and is what makes a cycling button legible. `auto` is never pressed, because auto is the
absence of a choice rather than a choice, and a pressed button says "this is on".

## Consequences

Two rules in the record are superseded in practice, and both are recorded in the design index's "Known
staleness" so a later reader finds them here rather than re-deriving them from a template that disagrees
with the document.

The cost of the first is small and stated: a reader's collapsed navigation is per-device rather than
per-account. The cost of the second is that §4.1's header row is not yet a header row — the banner
carries the campaign, the search field and the account, and no switcher. Both are phase 9's work, and the
hooks are in the markup now so that phase 9 is a rewrite of behaviour rather than a change of structure.

One consequence is worth isolating because it is a security property rather than a usability one. The
mode switcher writing the cookie client-side, and the server never reading it back into the document,
means `include_secrets` and access tier remain the *only* things the response varies by
([0035]({{ "decisions/0035-no-vary-cookie-and-one-blocking-script/" | relURL }})). A switcher that
submitted its value would be the first thing to reintroduce `Vary: Cookie`, and it would do so in a way
that looks entirely reasonable in review — the user chose a setting, the page reflects the choice — which
is why the rule is recorded here rather than left to be rediscovered.

## Alternatives considered

**Adding a third `sp_ui` field for the collapse, and calling it settled.** Rejected: §6.6 fixes the
schema in two fields and gives a reason that covers this case exactly. A third field would also be
readable by exactly the code that reads the other two, so it would have to be threaded through the
parser, the writer, the resolver's read-tolerance and the cookie's round-trip test — four places to keep
in step for a preference that is honestly per-device.

**Editing §4.3 and §6.6 in place.** Rejected by the immutable-records rule, and the rule exists because
in-place edits are indistinguishable from reinterpretation: a reader of the record cannot tell which
sentence was written when the record was wrong and which was written to be right. The correction lives
here, the staleness entry points at it, and the record still reads as a coherent statement of intent.

**Rendering the switchers now as disabled controls.** Rejected: §4.3's own principle is that edit
affordances are "absent, not disabled", and the reason given — a disabled control is a promise the page
cannot keep — applies identically to a switcher that cannot switch. A greyed-out popup is worse than no
popup, because it advertises a feature and then declines to provide it.

**Making the mode switcher a `<select>` that submits to the server.** Rejected: it makes the response
depend on `sp_ui`, which is the S-13.5 violation reached from the switcher side. The switcher is a
client-side write to the same cookie the head resolver reads, and that is the only shape of it that does
not put a theme preference into a cache key.