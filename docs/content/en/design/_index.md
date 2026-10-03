---
title: "Design records"
description: "The architecture and UI specifications, published verbatim."
lede: "Two specifications, published exactly as they were written, because a design document that has been retyped into a website eventually stops matching the design it describes."
weight: 50
---

These are **dated design records**, not user documentation. They are rendered
straight from the committed files in the repository, so the copy here cannot drift
from the original. The cost of that choice is that they are also unedited: they
contain open questions that have since been answered, and sections describing
behaviour the code does not have yet.

Read them for intent and for reasoning. For how to run the thing, read
[install]({{ "install/" | relURL }}) and [concepts]({{ "concepts/" | relURL }}).

## The records

### [Technical architecture]({{ "design/architecture/" | relURL }})

The product model, the data model, the content pipeline, editing and conflict
resolution, the realtime protocol, access control, the plugin system, the
security boundaries, and the build order.

Source: `.opencode/plans/1790774477695-semiplane-architecture-overview.md`

### [Responsive UI/UX specification]({{ "design/ui-ux/" | relURL }})

The five structural components, five width tiers plus a television mode, the
three-layer token system with both palettes, the accessibility contract, and the
validation strategy.

Source: `.opencode/plans/1790778908232-responsive-ui-ux-design-spec.md`

## Why verbatim

Three reasons, in order of weight:

1. **A retyped copy drifts.** Every future architecture edit would need a
   corresponding edit on the website, and the first one that gets forgotten turns
   the documentation into a confident lie.
2. **The value is in the reasoning, not the summary.** Both documents argue with
   themselves in writing — they state what was rejected and why. That is the part
   worth reading, and it is exactly what a summary removes.
3. **They are already public.** They are in the repository, so publishing them
   adds no exposure and no maintenance burden.

## Known staleness

Rather than fixing these in place — which would destroy the record's value as a
record — they are listed here:

- Both documents predate any implementation, so they describe intended behaviour
  throughout. The
  [roadmap]({{ "roadmap/" | relURL }}) says which phase each part lands in.
- The UI specification introduces a `world`/`session` game entity pair, which the
  architecture record had already removed. Where the two conflict, the
  architecture record is correct; the UI document's own terminology table flags the
  discrepancy.
- The server-sent-event routes in the UI specification predate the URL scheme
  settled in the architecture record.
- **The architecture record's `ETag` rule is a defect.** §5.5 says the validator
  is the content hash, and the same section defines two body variants from that
  one hash. A GM's unredacted response and a player's redacted one would
  advertise the same validator, and any cache holding both would serve whichever
  it stored first — a direct secret leak to a player. The correction is
  [0016]({{ "decisions/0016-salted-etag/" | relURL }}): salt the validator with
  the `include_secrets` flag.
- **The architecture record's build order cannot work as written.** §15 builds
  the templ shell at phase 5 but places the Tailwind toolchain at phase 10, and a
  shell cannot be styled before the CSS toolchain exists. The toolchain moves to
  the foundations phase and the shell becomes its own phase. See
  [0013]({{ "decisions/0013-templ/" | relURL }}).
- **The architecture record's `pages` table lists a `front_matter` column, and
  the table does not have one.** The filesystem is the source of truth, so the
  front matter is already in the file; a second copy of a block the parser
  regenerates in a millisecond is a second copy that can disagree with the
  first, and the failure is a per-page disagreement with no error anywhere. See
  [0027]({{ "decisions/0027-no-front-matter-column-and-the-index-is-a-cache/" | relURL }}).
- **The UI specification's `§4.3` and `§6.6` disagree about persisting the
  collapsed navigation.** §4.3 puts it in `sp_ui`; §6.6 fixes `sp_ui` to two
  fields and says "no density field — a dangling token is worse than no token".
  §6.6 governs: it is later, it defines the cookie's schema rather than one
  field's use, and its stated reason covers this case exactly. The collapse is a
  `data-nav` attribute, so it is per-device rather than per-account. The
  interface consequences of §4.3 — the 3.5rem icon rail, and each row keeping
  its full accessible name — are unaffected. See
  [0033]({{ "decisions/0033-two-conflicts-in-the-ui-record/" | relURL }}).
- **The UI specification's `§4.1` switchers are deferred to the client layer.**
  It specifies a campaign switcher as a popup menu and a mode switcher as a
  `<select>`; §3.7 requires one canonical DOM across tiers and forbids the
  document varying by `sp_ui`, so neither can be server-rendered: a popup needs a
  menu to open, and a `<select>` that submits its value would make the response
  depend on the cookie. What ships now is the campaign's own address as a link
  (which is also §8.3's rank 1) and a `data-chrome` hook on each control the
  client will take over. The mode switcher must write `sp_ui.ui` client-side;
  the server never reads it back into the document. See
  [0033]({{ "decisions/0033-two-conflicts-in-the-ui-record/" | relURL }}).
- **The UI specification's `§4.7` state table has ten rows where its own task
  list says eleven.** §7.5's live-region table attributes three conditions to
  §4.7, of which two are not designed; the eleventh is `ConnectionLost`, which
  has state behind it. The twelfth, "game ended", is deliberately absent — there
  is no domain state for it, and §4.9 says of exactly this case to flag it
  rather than invent a client-side flag. See the states package's header.
- **The records name `.kilo/plans/` and `kilo.json`, neither of which exists.**
  The repository has moved to OpenCode as its only agent: the design records now
  live under `.opencode/plans/` and `kilo.json` has been deleted in favour of
  `.opencode/opencode.jsonc`. The records are published here verbatim, so they
  still print the old paths — the file names, `make site-plans`'s source and the
  "Source of this page" links are all correct, and the prose inside the two
  records is not. This is the intended outcome rather than an oversight: editing
  a dated record to agree with the present would destroy the reason for keeping
  it. See
  [0051]({{ "decisions/0051-opencode-is-the-only-agent-and-its-directory-is-the-committed-one/" | relURL }}).
- Diagrams render as code blocks. They are readable as source, which is how they
  were written.

Where a record is wrong, the correction is a
[decision record]({{ "decisions/" | relURL }}), never an edit here. That is the
whole reason this list exists.
