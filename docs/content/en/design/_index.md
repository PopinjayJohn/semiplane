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

Source: `.kilo/plans/1790774477695-semiplane-architecture-overview.md`

### [Responsive UI/UX specification]({{ "design/ui-ux/" | relURL }})

The five structural components, five width tiers plus a television mode, the
three-layer token system with both palettes, the accessibility contract, and the
validation strategy.

Source: `.kilo/plans/1790778908232-responsive-ui-ux-design-spec.md`

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
- Diagrams render as code blocks. They are readable as source, which is how they
  were written.

Where a record is wrong, the correction is a
[decision record]({{ "decisions/" | relURL }}), never an edit here. That is the
whole reason this list exists.
