# semiplane — Responsive UI/UX Design Specification and Layout Strategy

**Status:** implementation-ready design specification, reconciled against architecture overview
revision 2. Each section ends with its task list; a consolidated build order is in `§15`.
**Date:** 2026-09-30 (reconciled). **Repo state:** scaffold only — `cmd/server`,
`internal/config`, `internal/httpapi` (bare mux, three handlers), empty `internal/domain`,
`internal/store`, `internal/web`. No templ, no Tailwind, no CSS.
**Companion:** `.kilo/plans/1790774477695-semiplane-architecture-overview.md` — the
*Technical Architecture Overview*, 1,058 lines. That plan owns domain semantics: the
terminology table (`§2.1`), roles (`§2.2`), the content pipeline (`§5`), secret callouts
(`§5.6`), conflict resolution (`§6`), the WebSocket protocol (`§7`), the plugin system (`§10`),
and the URL scheme (`§9`). **This document owns pixels and nothing else.** Where the two
disagree, the architecture plan wins and the disagreement is called out in-line.

### Reconciliation log

Revision 2 of the architecture plan changed things this spec had built on. What moved:

| Change | Effect on this spec |
|---|---|
| `world` and play-`session` entities **removed**; a campaign has at most one live tabletop; route is `/c/{slug}/play` | `§1.2` vocabulary rewritten; `§4.3` nav loses its Games list; `§4.4` route table fixed |
| Terminology fixed upstream (`§2.1`) | `§1.2` defers to the plan instead of inventing terms |
| Roles settled: **`gm`/`player`, GM-only content editing** | `§4.3`, `§4.5`, `§4.8` — edit affordances are absent for players, not disabled |
| New plugin system (`§10`): gameplay / UI / theme tiers, registry-backed `kind` | new `§4.11` plugin UI contract, `§4.12` theme layer contract; `§4.3` kind labels are registry-driven |
| New theme tier: "CSS, header background, fonts" from the content root | `§5.2` webfont ban lifted, conditionally; new `§4.12`; brand tokens added to `§6` |
| New `[!secret]` callouts (`§5.6`) | new `§4.10`; `§6.2`/`§6.3` gain callout tokens |
| Render cache keyed `include_secrets`; body no longer permission-neutral | **§3.7's central invariant was false.** Corrected in `§3.7`; new `ETag` and cache rules in `§6.6` |
| Plan contradicts itself on play-page transport (`§7` vs `§3.1`/`§9`) | resolved in `§7.5`; WS carries state and presence, SSE carries rendered sidebar fragments |
| Two new degraded states: unknown `system_id`, `ruleset_version` drift (`§10.8`) | added to `§4.7` |
| Plan `§15` builds the shell at phase 5 but Tailwind at phase 10 | **Plan defect.** Flagged in `§15.2`; this spec's Slice A must attach before phase 5 |
| Plan `§16.1` names this spec stale and a blocker on UI work only | reconciled here; phases 1–9 and 11 of the plan are unaffected |

---

## 0. Requirement traceability

| Requirement | Satisfied by |
|---|---|
| Persistent header | `§4.1` — `role="banner"`, every route, never collapses |
| Persistent footer | `§4.2` — `role="contentinfo"`, every route; becomes a bottom nav bar below 1024, and the player's action bar on `/play` |
| Left-side navigation sidebar | `§4.3` — persistent ≥1024, icon rail in short landscape, sheet below; absent only pre-campaign (`§4.6`) |
| Primary centre content area | `§4.4` — `role="main"`, one `<h1>`, the route slot |
| Right-side utility/information sidebar | `§4.5` — persistent ≥1280, bottom sheet below 640, drawer 640–1279 |
| 1. Responsive breakpoints | `§3` — five width tiers, TV as a mode (`§3.3`), orientation (`§3.5`), scroll model (`§3.6`) |
| 2. Theming, tokens, light + dark | `§6` — three token layers, two palettes, campaign brand layer (`§4.12`), no-flash switching, forced-colors |
| 3. Accessibility, WCAG, contrast, targets, keyboard | `§7` — AA target, AAA where stated, plus 10-foot and D-pad rules |
| 4. Component hierarchy across input methods | `§8` — four tiers, three input modalities, orientation, prominence rules |
| Editor + conflict resolution (plan `§1`, `§6`) | `§4.8` — textarea, split preview, 412 diff in the preview slot, no auto-save |
| Player participation on a phone | `§4.9` — read-only map, action bar, no positional play |
| Secret callouts (plan `§5.6`) | `§4.10` — reveal affordance, redaction, theming, the AT decision |
| Plugin system (plan `§10`) | `§4.11` — tokens are the plugin contract; `§4.12` — theme layer contract |

Terminology: this document says **rail** for the two sidebars and **centre** for the content
column — the brief's "left-side navigation sidebar", "primary center content area" and
"right-side utility sidebar". Only shorter.

---

## 1. Scope and vocabulary

### 1.1 Platform model

semiplane ships **one responsive web application** from the one Go binary. Laptop, phone and
television are browser contexts of the same URLs, not separate clients.

- No iOS, Android or tvOS codebase. **This does not amend the plan's out-of-scope list** — a
  responsive surface is not a "mobile client", and plan `§1` stands.
- Layout adapts on viewport geometry and input capability, never device identity. The one
  exception is the TV signal (`§3.3`), and it is user-overridable.
- Tailwind v4 (pinned by the plan) is the delivery vehicle, but every token here is a CSS
  custom property, so the spec survives a tooling change.

### 1.2 Vocabulary — the plan owns the terms, this section owns the labels

Plan `§2.1` fixed the domain vocabulary. That table is authoritative and is not restated
here. What follows is the *interface label* for each concept, and the labels the interface
must never invent.

| Domain term (plan `§2.1`) | UI label | Note |
|---|---|---|
| campaign | Campaign | unchanged |
| page | Page | unchanged |
| game object | its registered kind label | `kind` is **registry-backed** (plan `§4.1`, `§10.2.1`), so the label is data, not a string in this document — see `§4.11` |
| ruleset | "System: D&D 5e 2024" + house-rule count | shown in the campaign rail and header of `/play` |
| placement | the kind's label — a `token` placement is a **Token** | the map and the token list both say *Token*; a `scene` placement says *Scene* |
| campaign state | never surfaced | |
| auth session | never surfaced | |

| Interface concept | UI label | Note |
|---|---|---|
| the live tabletop | **Table** | the plan uses "table" in `§5.6.1`. **Not a domain entity** — a campaign has at most one, so there is no list of them |
| playing | Play | the route is `/c/{slug}/play`; the nav entry is "Table" |

**Hard rules**

- **The strings "world" and "session" appear nowhere in the interface.** Neither entity
  exists any more. This includes `aria-label`s, `title`s, empty states, and error copy. A
  leftover is a bug, and a grep for them is a test (`§10.2`).
- There is no Games list in the left nav. There is one Table per campaign, so the entry is a
  single link, not a list.
- `plan §7.1` still emits `"world_time"` in the clock frame. That is a **field name in the
  protocol**, not a UI string; the UI calls it the game clock.

### 1.3 TV capability

The TV is a **table display**. It opens the same `/c/{slug}/play` as every other client and
sends **view intents only**.

| TV may | TV may not |
|---|---|
| pan, zoom, reset view | edit content, or open the editor |
| spotlight or focus a placement | resolve combat, set HP, or reveal a secret |
| cycle map layers, toggle the contrast overlay | invite, remove, or change roles |
| page the chat and dice logs | sign in, sign out, manage the instance |
| disconnect and reconnect | own any authoritative state |

**TV capability is orthogonal to permission.** A GM who signs in on the TV sees the
unredacted variant of every page, exactly as on a laptop. What the TV cannot do is *perform*
a disclosure — a reveal is a GM action with its own audit row (`§4.10`), and a display device
is not a GM console. The row above says "cannot reveal", not "cannot see".

This preserves the server-authoritative model in plan `§7.2`: a shared-room display that
cannot mutate state cannot desynchronise a table, and a focus ring stuck on a TV cannot strand
a room mid-combat.

### 1.4 Out of scope

Native clients. Offline-first editing. Right-to-left and vertical writing modes. **Print
stylesheets entirely.** Human screen-reader audit — `§10` automates structure, not perception.

Plan `§16.1` classifies this spec as a blocker on UI work and **not** on plan phases 1–9 or
11. Nothing in this document blocks the backend.

**Tasks**

- [ ] Record the label table in `§1.2` in `AGENTS.md`; add "no `world`, no `session` in any user-visible string" to the review checklist.
- [ ] Add a grep-based test asserting neither word appears in rendered HTML.

---

## 2. Form factors and the viewport matrix

The primary target is a **14-inch laptop** — height-constrained, and the whole desktop layout
is budgeted against it:

| Property | 14" Windows | 14" MacBook |
|---|---|---|
| CSS viewport, maximised | 1366 × 768 | 1512 × 982 (scaled) |
| Device pixel ratio | 1 | 2 |
| Height lost to browser chrome | 80–100 | 90–110 |
| Centre height after header + footer | **~580–600** | **~790** |

Verification matrix — every row is a design and test obligation:

| Form factor | Viewport (CSS px) | Tier |
|---|---|---|
| Small phone portrait | 320 × 568 | compact |
| Phone portrait | 360 × 640, 390 × 844 | compact |
| Phone landscape | 844 × 390 | compact-short |
| Tablet portrait | 768 × 1024 | compact |
| Tablet landscape | 1024 × 768 | medium |
| **14" laptop (primary)** | **1366 × 768, 1440 × 900** | **large** |
| 14" MacBook | 1512 × 982 | large |
| Desktop | 1280 × 800, 1920 × 1080 | large / wide |
| Ultrawide | 2560 × 1080, 3440 × 1440 | wide |
| TV 1080p | 1920 × 1080, DPR 1–3 | tv |
| TV 4K reporting native CSS px | 3840 × 2160 | tv-wide |

**Tasks**

- [ ] Record this matrix as a fixture in the viewport sweep test (`§10.3`).

---

## 3. Breakpoints and how the layout shifts

### 3.1 Width tiers

| Token | Value | px | Tier | Left nav | Right rail | Footer |
|---|---|---|---|---|---|---|
| base | — | 0 | compact | drawer from left, `min(18rem, 85vw)` | **bottom sheet**, ≤70dvh † | fixed bottom bar 3.5rem ‡ |
| `--breakpoint-sm` | 40rem | 640 | compact | drawer | **bottom sheet** † | bottom bar ‡ |
| `--breakpoint-md` | 48rem | 768 | compact-short | 3.5rem icon rail, landscape only | bottom sheet † | none |
| `--breakpoint-lg` | 64rem | 1024 | medium | 13rem persistent | drawer from right + scrim, 22rem | inline 2rem |
| `--breakpoint-xl` | 80rem | 1280 | **large (primary)** | 15rem persistent | 20rem persistent | inline 2rem |
| `--breakpoint-2xl` | 96rem | 1536 | wide | 15rem persistent | 22rem persistent | inline 2rem |

† On `/play` at compact the rail sheet anchors **above** the action bar, never over it
(`§4.9`). ‡ On `/play` at compact the bottom bar is the player's action bar, not the nav
destinations (`§4.9`).

**The right rail is a bottom sheet below 640px, not a side drawer.** A 20rem (320px) drawer
and an 18rem (288px) left drawer cannot coexist on a 360px screen — together they exceed it by
250px — and a single 320px drawer on a 360px screen leaves a 40px sliver and no way to see
the page behind the scrim. Below 640px **both** rails are bottom sheets and the two side
edges go unused.

Three rules govern the shift:

1. **The right rail persists last and collapses first.** It is the utility panel; on `/play`
   it holds the initiative tracker, which a GM needs *glanceable*. It persists from 1280, the
   breakpoint chosen so both 1366 and 1512 get three columns.
2. **Rails become sheets, never icon rails, below 1024** — except short-landscape (`§3.5`),
   where vertical space forces an icon rail.
3. **No tier is a scaled version of another.** Each re-decides what persists, what becomes a
   sheet, and what the primary action is. `transform: scale()` is prohibited (`§5.3`).

### 3.2 Component behaviour per tier

| Component | compact (<640) | compact-short | medium (1024–1279) | large (≥1280) | wide (≥1536) |
|---|---|---|---|---|---|
| Header | 3.5rem: menu, title, search, account | 2.5rem: menu, title, search | 3.5rem + inline search | 3.5rem full | 4rem, wider field |
| Left nav | bottom sheet 18rem | 3.5rem icon rail | 13rem persistent | 15rem, user-collapsible to 3.5rem | 15rem |
| Centre | full width, page scrolls | flex, scrolled region | ~816px, `min(100%, 68ch)` | flex, `min(100%, 68ch)` | flex, `min(100%, 72ch)` |
| Right rail | bottom sheet ≤70dvh | bottom sheet ≤85dvh | right drawer 22rem + scrim | 20rem persistent, independent scroll | 22rem persistent |
| Footer | fixed bottom bar + `safe-area-inset-bottom` | none | inline 2rem | inline 2rem | inline 2rem |
| Shell scroll | document-scrolled | shell-locked | shell-locked | shell-locked | shell-locked |

**Height budget at 1366 × 768:** header 56 + footer 32 = 88, leaving ~580px of centre.
Centre width 1366 − 240 − 320 = 806px. The article column is `min(100%, 68ch)` ≈ 636px at a
17px base, so ~85px of gutter per side — **the leftover is gutter, never wider prose.** The
map takes the full 806px, letterboxed to its own aspect ratio rather than distorted.

### 3.3 TV is a mode, not a width band

A 55" television and a 12.9" tablet in landscape are **indistinguishable to CSS**: coarse
pointer, never hover, wider than 1280px. They need opposite layouts — 10-foot type at 3m
versus 2-foot type at 30cm.

**Detection, in priority order:**

1. `?ui=tv` (or `?ui=laptop`, `?ui=phone`) — wins, and is bookmarkable.
2. The `sp_ui` cookie — the persisted choice, set by the on-screen mode switcher.
3. A narrow user-agent **hint allowlist** — `SmartTV`, `Tizen`, `webOS`, `HbbTV`, `Web0S`,
   `NetCast`, `AppleTV` — used **only** for the first-visit default, before a cookie exists.
   It never overrides (1) or (2).
4. Otherwise media queries alone → laptop layout.

`prefers-reduced-motion`, `prefers-contrast` and `forced-colors` are honoured in every mode
and never used to infer a form factor.

**Accepted failure mode:** an unrecognised TV browser shows the laptop layout until someone
flips the switcher — one remote press, persisted thereafter. Correct trade against a stale UA
list, and against a tablet misclassified as a television.

### 3.4 TV layout

```css
/* [data-ui="tv"] */
.shell {
  grid-template-areas: "header header" "main rail" "footer footer";
  grid-template-rows: 5rem minmax(0, 1fr) 3rem;
  grid-template-columns: minmax(0, 1fr) 26rem;
}
```

- **No left rail.** D-pad traversal of a 40-entry wiki tree is punishing. Navigation becomes
  a horizontal focusable strip in the header (Home · Wiki · Search · Table · Settings) plus a
  campaign tile grid on the home route.
- Map centre ≈1504 × 968; right rail 26rem.
- At 3840 CSS px (`--breakpoint-tv-wide: 137.5rem`), `--type-scale` rises to 1.75 and
  `--rail-right` to 30rem. The centre is capped, never allowed to run to the raw edge.
- 4K TVs reporting DPR 2 see a 1920 × 1080 CSS viewport and are unaffected.

### 3.5 Orientation

| Case | Rule |
|---|---|
| Phone portrait → landscape | Rails stay bottom sheets. Centre scroll position and any unsaved editor buffer survive. |
| Short landscape (`max-height: 480px`) | Header collapses to 2.5rem, bottom bar removed entirely, left nav becomes a permanent 3.5rem icon rail. A 390px-tall viewport cannot afford 56 + 56 of chrome. |
| Tablet landscape | Treated as `medium`. |
| Laptop resize across a breakpoint | Reflows with no modal, no reload, no loss of editor buffer. |

### 3.6 Shell scroll model

At `medium` and above the shell is **viewport-locked**: `height: 100dvh; overflow: hidden` on
the grid, `overflow-y: auto` on the centre and each rail, `overscroll-behavior: contain` so a
rail reaching its end does not chain-scroll the page.

At `compact` the shell is **document-scrolled** — one page scroll, because a nested scroll
region inside a mobile viewport fights momentum scrolling and the URL bar. Bottom-bar space
is reserved with `padding-block-end`.

`dvh` throughout, never bare `vh`; `vh` appears only as a fallback declaration preceding
`dvh`.

### 3.7 Two root attributes, resolved before first paint

The whole responsive system hangs off two root attributes:

| Attribute | Values | Set by |
|---|---|---|
| `data-theme` | `light` \| `dark` | the inline head script, from cookie or `prefers-color-scheme` |
| `data-ui` | `compact` \| `compact-short` \| `medium` \| `large` \| `wide` \| `tv` \| `tv-wide` | the same script, from cookie, `?ui=`, UA hint, then media queries |

One blocking inline script in `<head>`, before the first paint, resolves both. Consequences,
all deliberate:

- **The document still does not vary by `sp_ui`.** Therefore **no `Vary: Cookie` is emitted
  and none is needed.** This is the load-bearing conclusion of the earlier revision and it
  survives: plan `§5.5`'s "`ETag` = the same hash" is about the *content* hash, and the
  content hash is not a function of a theme preference.
- All tiers and both themes are CSS variants of one canonical DOM, so tiers cannot drift.
- A no-JS visitor gets the OS polarity from `<meta name="color-scheme" content="light dark">`
  and `color-scheme: light dark` on `:root`, losing the cookie override but not coherence.
- This is the only JavaScript on the critical path.

**What the document *does* vary by** — corrected against plan `§5.5` and `§5.6`:

| Varies by | Why | Consequence |
|---|---|---|
| access tier | chrome differs per request (plan `§6.5`) | already true in revision 1 |
| `include_secrets` | two body variants from one `content_hash` (plan `§5.5`) | **the body is no longer permission-neutral.** See the `ETag` and cache rules in `§6.6` — this is a security boundary, not a caching nicety |
| campaign | each campaign may serve its own theme stylesheet (`§4.12`) | `<link>` to `/c/{slug}/theme.css`, server-rendered |

**Tasks**

- [ ] Declare every layout constant as a custom property so the grid and component queries read one source.
- [ ] Write the inline head resolver: `sp_ui`, then `?ui=`, then the UA hint list, then media queries; writes both attributes; writes the cookie when `?ui=` or the switcher is used. Inline, synchronous, under ~1KB.
- [ ] Emit `<meta name="color-scheme" content="light dark">` and `color-scheme` in the root token block.
- [ ] Serve the below-640px rails as bottom sheets, re-parenting on resize rather than re-rendering.
- [ ] Assert in the viewport sweep that no horizontal scrollbar exists at 320px and both sheets leave ≥40px of context.

---

## 4. The shell: five structural components

```
┌───────────────────────────────────────────────────────────────┐
│ header   banner — campaign · search · connection · theme · user │  56px
├──────────────┬─────────────────────────────┬──────────────────┤
│ nav          │ main                        │ aside            │
│ Campaign     │  route slot: article | map |  complementary    │
│ Wiki         │  editor | search | tables   │  route slot      │  ~580px
│ Table        │                             │  TOC · table     │
│ Admin        │                             │  panel · facets  │
├──────────────┴─────────────────────────────┴──────────────────┤
│ footer  contentinfo — version · status · latency · degraded    │  32px
└───────────────────────────────────────────────────────────────┘
```

```css
.shell {
  display: grid;
  height: 100dvh;
  overflow: hidden;
  grid-template-areas: "header header header" "nav main rail" "footer footer footer";
  grid-template-rows: var(--header-h) minmax(0, 1fr) var(--footer-h);
  grid-template-columns: var(--rail-left) minmax(0, 1fr) var(--rail-right);
}
```

`minmax(0, 1fr)` is load-bearing: a plain `1fr` track blows out to `min-content` on any long
unbroken string in a heading. `grid-template-areas` is used rather than `subgrid`; no
load-bearing rule uses `:has()` or container queries, because old TV browsers are a supported
target (`§9`).

### 4.1 Header — `role="banner"`

| Zone | Contents |
|---|---|
| Left | Menu button (compact only); campaign switcher; mode switcher |
| Centre | `<form role="search">` with `<input type="search" name="q">`, submitting to `/c/{slug}/search` |
| Right | Connection indicator (live routes only); theme switcher; account menu |

- Campaign switcher is a `<button aria-haspopup="menu">` + `role="menu"` popup on pointer
  tiers, a native `<select>` on TV.
- The **mode switcher** (`§3.3`) is a `<select>` listing auto / laptop / phone / TV; on TV it
  is the first tile on the home grid, because a remote cannot open a popup menu reliably.
- Theme switcher is a **single button cycling auto → light → dark**, with `aria-pressed` and a
  label naming the *result* ("Theme: auto"), never the action.
- Connection indicator: `role="status"`, `aria-live="polite"`, and a **text** label
  ("Live", "Reconnecting", "Offline") — never colour alone.
- The **skip link is the first focusable element in the document** (`§7.2`).

### 4.2 Footer — `role="contentinfo"`

Exactly one per page, `/play` included. A deliberate adaptation of "persistent footer", not a
removal: at `compact` it *becomes* the primary navigation, because a thumb cannot reach the top
of an 844px screen.

| Tier | Content |
|---|---|
| Desktop | version/commit, instance status link, live latency, degraded-campaign warning |
| Compact | fixed bottom bar, `3.5rem` + `env(safe-area-inset-bottom)`, `role="navigation"`, `aria-label="Primary"`: Wiki · Search · Table · More. On `/play` the four are **replaced by the player's action bar** (`§4.9`). |
| `/play` | game clock, connection state, "Leave table" |

**Instance status** is a footer link on every tier, not just `/admin`. Plan `§13.2` names four
subsystems that fail silently by default — a watcher that stops watching, a cache serving a
stale tier, a reconciliation loop that quits, a hub that leaks connections. A GM needs to see
`secret.reconcile_capped` without a shell. The status view renders the counters `/readyz`
exposes as JSON as a table, using the same component as `/admin` (`§7.8`).

The degraded-campaign banner lives here permanently, not as a toast: a campaign whose content
root has gone missing is an ongoing condition, not an event.

### 4.3 Left nav — `role="navigation"`, `aria-label="Campaign"`

Sections in order: **Campaigns**, **Wiki**, **Table**, **Search**, **Admin**.

- **Table is a single link, not a list.** A campaign has at most one live tabletop
  (plan `§2.1`), so there is nothing to enumerate. The link is present for members and absent
  for anonymous readers.
- **Edit affordances are absent, not disabled.** Plan `§2.2` settles roles as
  `gm | player` with GM-only content editing. A `player` and an `anonymous` viewer get no
  "New page", no edit link, and no `kind`-creation control anywhere in the DOM. This is
  `§4.5`'s server-side-absence rule made concrete.
- **Nested lists of links, not `role="tree"`.** A tree pattern obliges roving tabindex,
  arrow-key semantics and typeahead — machinery that buys nothing for a server-rendered list
  changing on every navigation, and that would need re-implementing for every disclosure.
  Nested `<ul>` inside a labelled `nav` makes every item a tab stop: predictable, robust, no
  JavaScript. The cost is a long tab sequence, mitigated by the skip link and capped depth.
- Folder expand/collapse is a `<button aria-expanded>` on the row, not a tree node.
- **Game-object and kind labels are registry-driven, not hard-coded** (`§4.11`). The wiki tree
  renders a `kind` badge whose label comes from the registry, so a UI plugin registering a new
  kind needs no shell change.
- User may collapse to a 3.5rem icon rail, persisted in `sp_ui`. At ≤1279 it is not
  user-collapsible — no room for a collapsed state that still works. Each icon link keeps its
  full name in `aria-label`; the tooltip is `aria-hidden` decoration.

### 4.4 Centre — `role="main"`, one `<h1>` per page

The centre is a **slot**. Each route declares what it puts there. This contract is what keeps
five structural components from becoming five special cases.

| Route | Centre | Right rail (`complementary`) |
|---|---|---|
| `/` | campaign list, or first-run empty state (`§4.7`) | instance status: degraded campaigns, version |
| `/login` | credentials form, centred | instance identity, version, degraded warning |
| `/c/{slug}` | campaign overview, recent changes | **system + house rules**, roster, who is online |
| `/c/{slug}/wiki/{path...}` | rendered page, secrets redacted for non-GM (`§4.10`) | page outline (h2/h3), metadata, sibling pages, revisions link |
| `/c/{slug}/edit/{path...}` | **GM only** — editor, 412 diff in the preview slot (`§4.8`) | outline, external-change indicator, save state, revision list |
| `/c/{slug}/secrets/{path...}` | **GM only** — reveal/unreveal, `If-Match` (plan `§5.6`); no page of its own, a POST target | — |
| `/c/{slug}/search` | result list, submit-to-navigate (`§7.5`) | facets, saved searches |
| `/c/{slug}/settings` | **GM only** — tabs: Rules · Members · System | instance status, audit entries |
| `/c/{slug}/status` | instance status table (plan `§13.2`) | — |
| `/c/{slug}/play` | PixiJS map; **read-only readout at compact** (`§4.9`) | **tabbed**: Tokens · Initiative · Chat · Dice |

`/c/{slug}/settings` is an addition: the plan's `§9` enumerates content routes, not the app's
whole route surface (it has no `/login` either). House rules need a GM surface — plan `§10.5`
gives them ordering and conflict resolution, and a campaign with unorderable house rules has no
explanation anywhere else.

### 4.5 Right rail — `role="complementary"`, `aria-label="Utilities"`

Persistent at ≥1280, right drawer at 640–1279, bottom sheet below 640. As a sheet or drawer it
is modal: focus trapped, `Escape` closes, focus returns to the trigger, content behind is
`inert`.

**Server-rendered, not client-filtered: a tier's panels are absent from the DOM for viewers
below that tier, never CSS-hidden.** Plan `§6.5` lets chrome vary per request, which makes
server-side absence simpler *and* safer than a client role check — a hidden GM panel is one
CSS rule away from being visible.

The same rule, harder, applies to secrets: plan `§5.6.1` requires the body to be **absent**,
not `display:none`, not commented out, not a class. The UI honours that literally (`§4.10`).

### 4.6 Pre-campaign routes

Before a campaign exists, **the left nav is absent; header, centre, right rail and footer all
render.** The left nav is entirely campaign-scoped and its content model is undefined with zero
campaigns. The persistent header/footer requirement is never violated, and the right rail
still earns its place: instance identity, version, degraded warning.

`/`, `/login` and first-run use `"header header" "main rail" "footer footer"`.

Note that a user may exist with zero campaigns — plan `§13.1` provisions the first user via
`semiplane admin create` before any browser session. First-run is a *campaign* empty state,
not a *user* one.

### 4.7 Empty, error and degraded states

Every one is a designed surface using the full shell.

| State | Centre | Right rail |
|---|---|---|
| First run, zero campaigns | one primary CTA, "Register your first campaign", two lines of explanation, a docs link. Not a marketing page — this is a self-hosted tool. | instance identity, version |
| Campaign with no pages | "This campaign has no pages yet" + GM-only "Create a page" | roster, empty outline |
| Search, no query | search field, focused, with a hint | facets, empty |
| Search, no results | "No results for *q*", query echoed back | facets, empty |
| `403` | "You do not have access to this campaign" + the campaign name, so the message is specific | empty |
| `404` | "No page at *path*" + a link to the campaign root, never a raw server path echo | empty |
| `412` | not an error page. The conflict diff (`§4.8`) in the editor's preview slot | unchanged |
| `500` | request ID from the RequestID middleware (plan `§3`) | empty |
| **Unknown `system_id`** (plan `§10.8`) | the wiki **still serves**. The Table link is absent, not disabled, and the campaign overview carries a persistent notice: "This campaign's gameplay system (ID) is not installed. The wiki is unaffected." | system panel shows the missing ID |
| **`ruleset_version` drift** (plan `§10.8`, `§16.5`) | an `role="alert"` block on `/play`: "This table was last played under a different rules version. Resuming is blocked." with a link to the campaign's Rules tab. Default is refuse (`§16.5`) and the UI states the refusal as a decision, not an error. | system panel shows both versions |

Both new states are `assertive`-adjacent: a GM who expected a playable table must be told
plainly. Neither is a toast — both are conditions that persist until someone fixes them.

A `404` and a private campaign must not be distinguishable to an anonymous user, or the error
page becomes a campaign-existence oracle. Both return the same body shape.

**Tasks**

- [ ] Create the shell template with the five landmarks, the two-column pre-campaign variant, and `centre` / `rail` slots.
- [ ] Define the slot contract as a Go value (`domain.PageLayout{ Centre, Rail }`) so handlers declare layout without touching markup; `domain` stays free of I/O.
- [ ] Build header, footer, left nav and right rail as separate templ components taking a `domain.ShellPrefs`.
- [ ] Add `?ui=` handling and the inline resolver.
- [ ] Render all eleven states in `§4.7`, each with a `data-testid`.

---

## 4.8 The editor surface

The editor is a **plain `<textarea>` with a split preview** — not a code editor, not a rich
editor. It is the most accessibility-sensitive component in the app, and a textarea is correct
for keyboard and screen reader for free, with no JavaScript and no dependency.

```
┌─────────────────────────────────────────┬──────────────────────┐
│ toolbar  role="toolbar"  (Bold Italic   │                      │
│          Link Wikilink Code Heading     │  preview             │
│          Statblock Dice)                │  (server-rendered)   │
├─────────────────────────────────────────┼──────────────────────┤
│ <textarea>  --font-mono, spellcheck=off │  aria-live="off"     │
│                                          │                      │
├─────────────────────────────────────────┴──────────────────────┤
│ status  Idle · Saving · Saved · Conflict · Error · Ln 12, Col 3│
└──────────────────────────────────────────────────────────────┘
   ≥1024: two columns          <1024: Edit | Preview tabs
```

| Decision | Rule |
|---|---|
| Access | **GM only** (plan `§2.2`). The route is absent for `player` and `anonymous`; there is no read-only editor mode. |
| Element | `<textarea>` with a real `<label for>` ("Page content") and a description listing the extensions (`[[wikilink]]`, `![[embed]]`, `{{statblock}}`, `{{dice}}`, `[!secret]`, per plan `§5.4`). Never a placeholder-only field. |
| Attributes | `spellcheck="false"`, `autocapitalize="off"`, `autocorrect="off"`, `autocomplete="off"`. Markdown is not prose. |
| Line numbers | a sibling `aria-hidden="true"` gutter. The textarea cannot render them, and they must never be the only report of position — the status line carries `Ln`/`Col` as **plain text, not a live region**, so a cursor move does not interrupt. |
| Toolbar | `role="toolbar"`, roving `tabindex`, arrow traversal, real `<button>`s with `aria-label`. Insertion uses **`setRangeText()`**, not `.value` — a `.value` assignment destroys the native undo stack, and losing undo in an editor whose whole conflict story is "you can always go back" is unacceptable. |
| Preview | rendered **by the server**, not a client markdown library. Debounced ~800ms after the last keystroke plus on demand, `POST` to a render endpoint returning sanitised HTML from the same goldmark pipeline that produces the published page. A client renderer would have to reproduce five custom extensions exactly and could not, so the preview would lie. |
| Preview and secrets | the preview is rendered with `include_secrets=true` — the GM's buffer is their own content, and the editor is not a disclosure surface. See `§4.10`. |
| Preview a11y | `aria-live="off"`. A preview re-rendering on a timer inside a live region is a screen-reader storm. |
| **Never auto-save** | Save is explicit (`Ctrl`/`Cmd`+`S` plus a button). Auto-save makes the `If-Match` model incoherent: it would commit a `412` the user never saw, which is exactly what plan `§6.1` exists to prevent. |
| Save state | `Idle` / `Saving` / `Saved` / `Conflict` / `Error` in a `role="status"` region, plus `aria-busy` on the textarea while saving. |
| External change | SSE `/c/{slug}/events` raises a `role="status"` banner, "This page changed on disk", with a **Review** button that loads current disk content into the diff. Never auto-merges, never discards the buffer. |
| `412` conflict | **replaces the preview pane** at ≥1024 with a two-column diff (your buffer vs disk), each column independently scrollable, per-hunk accept/reject, then save with `If-Match` set to the new hash. Below 1024, a full-height layer above the Edit/Preview tabs. |
| Buffer safety | survives orientation change, resize across breakpoints, tab switch, and `412`. Never discarded without an explicit confirmation naming the loss. |

**Tasks**

- [ ] `POST /c/{slug}/render` returning sanitised HTML from the publication pipeline; assert the response cannot contain unescaped author HTML.
- [ ] Build the toolbar with `setRangeText` insertion; add a test that the native undo stack survives a toolbar action.
- [ ] Add the 412 diff view in the preview slot, with per-hunk accept/reject and `If-Match` on the follow-up save.
- [ ] Add the SSE external-change banner; assert the buffer is untouched when it fires.

## 4.9 The phone player surface on `/play`

A phone player is a **full participant** (unlike the TV, `§1.3`). At coarse-pointer tiers the
map is a **read-only readout**: it renders, it never takes a gesture, and it never reaches the
bottom of the screen.

```
┌──────────────────────────────┐
│ header  table name · live    │  3.5rem
├──────────────────────────────┤
│   map (readout, letterboxed) │  flex, no interaction
│   canvas aria-hidden         │
├──────────────────────────────┤
│ action bar                   │  4.5rem + safe-area
│ [token] [Roll] [End turn] [Chat]
└──────────────────────────────┘
   utility rail opens as a sheet anchored ABOVE the action bar
```

| Rule | Detail |
|---|---|
| Action bar replaces nav | on compact `/play` the persistent footer **becomes the player's action bar** rather than the 4-destination nav (`§4.2`). Height 4.5rem — larger than the 3.5rem nav bar, because these are the primary actions. |
| Controls | **Token** chip (which placement you are acting as; opens the Tokens sheet) · **Roll** (primary; a die sheet for the system's grammar, per plan `§10.3` `Grammar()` — *not* a hard-coded d20) · **End turn** (enabled only on your turn, disabled with a stated reason) · **Chat**. |
| No positional play | **there is no Move control on a phone.** Positional interaction exists at fine-pointer tiers and on TV via the token list (`§7.6`). A phone player reads positions and acts through discrete intents. Stated plainly because a player will look for it, and an absent control is worse than a disabled one with a reason. |
| Layer collision | the compact rail is a bottom sheet **anchored above the action bar** (`inset-block-end: var(--action-bar-h)`), never over it. The action bar is never obscured, at any tier. |
| No TV action bar | the TV is view-only; its view intents live on the map, and `End turn` is a player action. |
| Gesture safety | because the map takes no input at this tier, there is no drag-versus-bottom-bar or drag-versus-sheet conflict. |
| Dependency | `roll` and turn-end must exist in the plan's `§7.1` intent set. If turn-end is not an op, the UI cannot ship without a plan amendment — flag it, do not invent a client-side turn flag. |

**Tasks**

- [ ] Implement the compact action bar; assert it replaces the nav on `/play` only.
- [ ] Assert the compact map canvas carries `aria-hidden="true"` and no pointer handler.
- [ ] Anchor the compact rail sheet above the action bar; assert it is never covered at 320 × 568.

## 4.10 Secret callouts — the disclosure surface

Plan `§5.6` adds `[!secret]` callouts. It names four UI consequences; this section is all four.
The governing rule is plan `§5.6.1`: **the secret body is absent from the response for
non-GM viewers — not `display:none`, not a comment, not a class.**

### 4.10.1 The accessibility decision

**A redacted secret is not announced to assistive technology, and nothing is left in its
place.**

When the stub is off (the default), the callout is **not in the DOM at all** — no element, no
`aria-hidden`, no placeholder, no `role="presentation"`. A screen-reader user and a sighted
user therefore receive *identical* information, which is the entire point of omission.

The consequence must be stated rather than discovered: a sighted player may notice a seam in
the prose where a callout used to be, and a screen-reader user will not perceive it. **That
asymmetry is the correct behaviour under the default**, and it is precisely why plan `§5.6.1`
offers an opt-in stub — the GM chooses to close the gap at the cost of disclosing existence.

The alternative — announcing "a GM-only note was removed" — is rejected: it hands every
non-GM an oracle for where secrets are, which plan `§5.6.1` already identifies as itself
disclosing information.

### 4.10.2 The opt-in stub

Per-campaign, off by default, per plan `§5.6.1`. When on, each redacted callout renders an
empty `<blockquote>` carrying one non-secret label, "A GM-only note", in `--callout-secret`
styling. It is a real, readable, focusable-free block — screen-reader users get the same thing
sighted users get. No per-user or per-role variation: the choice is the campaign's.

**Prose-gap warning in the editor.** A secret at a block boundary leaves a clean gap. Mid-
paragraph, omission leaves a sentence that reads oddly. The editor raises a **non-blocking
notice** on any `[!secret]` callout not starting at a block boundary, and a stronger one when
the campaign's stub setting is off. It never blocks a save.

### 4.10.3 The GM reveal affordance

Reveal is **not** a save. Plan `§5.6.4` makes it a disclosure action with its own audit row,
its own ledger row, and its own `If-Match` endpoint (`/c/{slug}/secrets/{path...}`). It gets
its own control, in the editor's **preview pane** — where the GM already sees the secret:

| Control | Behaviour |
|---|---|
| Per-callout **Reveal** / **Unreveal** | a button inside the callout's rendered chrome, GM tier only, in the preview. Confirm on reveal: it is visible to every member. |
| **Reveal all on this page** | with a confirmation naming the count. One click rewrites N markers; each is a one-byte diff, which is what makes this safe against Obsidian (plan `§5.6`). |
| Stale `If-Match` | a reveal can `412` exactly like a save, because it is a file write. It routes to the **same** conflict diff (`§4.8`), not a second implementation. |
| Audit | after a successful reveal, a `role="status"` line names the audit consequence: "Revealed. Logged to the audit trail." |
| Reconcile-capped | plan `§13.2`'s `secret.reconcile_capped` is an **error**-level event because the system *chose* to hide something. It surfaces on `/c/{slug}/status` and, for a GM, as a `role="alert"` banner on `/play` — never silently. |

**Non-GM viewers get no reveal control, no placeholder for one, and no tooltip.** Server-side
absence again (`§4.5`).

### 4.10.4 Theming, and the revealed marker

`[!secret]-` (hidden, GM-only) and `[!secret]+` (revealed, public) need distinct treatments, and
the distinction is gameplay-critical, so **it is never hue-only**.

| Variant | Tokens | Treatment |
|---|---|---|
| hidden (GM only) | `--callout-secret` | recessive on purpose — a GM scanning a page mid-session should not be drawn to unrevealed secrets. `--surface-sunken` ground, `--text-muted` body, **dashed** `--callout-border` bar. |
| revealed (public) | `--callout-revealed` | `--accent-surface` ground, `--accent` body. Indistinguishable in content styling from an ordinary callout, so prose reads naturally. |
| "Revealed" marker | `--callout-revealed-marker` | a text marker, **on in the editor, off on the published page**, per campaign. Fiction breaks around a "Revealed" label during play; a GM auditing wants it. |

The marker is a text label, not an icon, because it is the one place the UI must be understood
without sight *or* without colour perception.

### 4.10.5 Search

`body_plain` excludes callout content for **every** role (plan `§5.6.5`). So a GM searching
for a secret's text gets nothing, and the GM's own editor preview is the only place the text
is findable. The UI must **not** promise a "3 results hidden because they are secret"
affordance — the backend cannot produce that count. Document it instead; do not build it.

**Tasks**

- [ ] Reveal/unreveal controls in the preview pane, GM only, with confirmation and the same 412 path as save.
- [ ] Assert no reveal control appears in any non-GM response.
- [ ] Add the block-boundary notice and the gap warning.
- [ ] Add `secret.reconcile_capped` to the `/play` GM alert banner and to `/c/{slug}/status`.
- [ ] Add an a11y test asserting the secret text and any stub label are absent from the player DOM *and* from the accessibility tree.

## 4.11 Plugin UI contract

Plan `§10` adds three plugin tiers. Only the UI tier and the theme tier touch this document,
and both impose hard obligations on the shell.

| Tier | UI consequence |
|---|---|
| Gameplay | none directly. It owns `ContentKinds()` and the `op` vocabulary; the shell is system-agnostic by design (plan `§10.2.1`) |
| UI | ships templ components, page `kind`s, render hooks, Datastar observers — **and inherits this accessibility contract** (`§4.11.1`) |
| Theme | a campaign stylesheet — **and is constrained** (`§4.12`) |

### 4.11.1 Tokens are the plugin contract

A plugin component **must** consume layer-2 semantic tokens (`--text-*`, `--surface-*`,
`--accent*`, `--border*`, `--callout-*`) and must use the `.target` utility for interactive
elements. It may not define its own colour constants. If a plugin genuinely needs a new
colour it declares **its own** `--plugin-*` tokens in its own block and passes the same
contrast test — a plugin may add tokens, never bypass the floor.

**The structural accessibility test applies to plugin output unmodified.** A plugin page type
that renders a second `<h1>`, a positive `tabindex`, an unlabelled landmark, or an
`outline: none` is a conformance failure, not an exempt island. Concretely:

- A plugin page type renders **into the centre slot**. The shell owns the landmarks; a plugin
  owns content.
- A plugin cannot add or remove a landmark. It contributes an `<h2>` or deeper.
- Plugin static assets register **declaratively** at the composition root. Plan `§11` forbids
  `init()`-based registration for Go packages; the same reasoning applies to a plugin's client
  files, which must be listed, not injected by a side effect.

### 4.11.2 A plugin control is an intent, like any other

Plan `§10.2` — a UI plugin may only emit operations a gameplay system already resolves — and
`§10.6` — it cannot write `campaign_state`. So a plugin's interactive control is
indistinguishable in the authorization path from a human's. Two UI consequences:

- Plugin controls obey the same target-size, focus and D-pad rules, and participate in the
  same `role="toolbar"` / list / grid patterns. There is no "plugin widget" exemption.
- **The worked consequence in plan `§10.6` is a testable UI rule.** The graphical dice roller
  must not roll client-side, because a client-side roll is unverifiable. So the die widget
  shows a **pending** state from send until the server's `applied` frame arrives, and a
  client-side result is never displayed — not even optimistically. An optimistic roll would be
  the single most damaging thing a plugin could do to the audit trail.

### 4.11.3 Registry-driven labels

`kind` is **registry-backed** (plan `§4.1`, `§10.2.1`), not a fixed enum. So the shell never
hard-codes a kind label: the wiki tree's kind badge, the create-page form's type list, the
create-placement control, and the `/play` rail tab names are all **data-driven from the
registry**. A UI plugin registering a new `kind` needs no shell change — and an unknown `kind`
degrades to prose with the label "Page", never an error.

**Tasks**

- [ ] Render kind labels from the registry everywhere; assert the shell contains no hard-coded kind string.
- [ ] Extend the structural a11y test to run against each registered plugin page type.
- [ ] Add the pending-then-applied rule for any plugin that emits `roll`; assert no client-side result is ever displayed.
- [ ] Require declarative registration of plugin client assets.

## 4.12 The theme layer contract

Plan `§10.1` defines a third tier: "plain files in the campaign content root — CSS, header
background, fonts". That directly contradicts revision 1 of this spec, which specified system
stacks and no webfont. Resolved: **the theme layer is real, and it is token-constrained.**

### 4.12.1 Overridable and not

| Overridable by a campaign | Not overridable — ever |
|---|---|
| `--brand-accent`, `--brand-accent-ink` | `--text-*`, `--border*`, `--focus-ring`, `--callout-*` |
| `--brand-header-image` | `--type-scale`, `--space-scale`, `--target-min` |
| `--font-prose` / `--font-ui` via `@font-face` (system stack always retained as fallback) | `--dur-*`, `--radius-*` |

The right-hand column is the accessibility contract. A campaign stylesheet that could set
`--focus-ring` or `--target-min` could silently break 1.4.11, 1.4.3 or 2.5.5, and no gate
could catch it across every campaign.

### 4.12.2 Enforcement is server-side, by construction

**A campaign cannot ship arbitrary CSS.** The content root carries a *theme manifest*; the
server validates it and **generates** `/c/{slug}/theme.css`. The protected variables above are
never emitted into that stylesheet, so a campaign cannot set them even by writing a raw
`.css` file — a raw file in the content root is served as a *static asset* under
`/c/{slug}/assets/` with `Content-Type: text/css`, and is never linked from the shell.

The manifest is attacker-reachable (Obsidian sync writes it), so it gets the same treatment as
front matter (plan `§12`): YAML parsed with alias-expansion limits, font `src` paths resolved
inside `os.Root` and rewritten to `/c/{slug}/assets/…`, `font-display: swap` forced,
document size capped.

### 4.12.3 The brand pair is validated, and fails toward the default

At registration, and again whenever the watcher sees the manifest change, the server checks
`--brand-accent` against `--brand-accent-ink` at ≥4.5:1, and `--brand-accent` against `--bg` at
≥3:1. On failure:

1. the last known-good theme stays in effect, or the core theme if there never was one;
2. a `theme.brand_invalid` **error** event is logged with `campaign_id` — plan `§13.2`'s
   principle, since a silently unreadable brand colour is a silent failure;
3. the campaign overview shows a GM notice naming the rejected pair.

**A rejected theme degrades to the default. It never ships an unreadable UI.** This is the same
fail-toward-safety direction as plan `§5.6.2`'s reconciliation cap.

### 4.12.4 Per-campaign, not per-user

A campaign theme is a property of the campaign. It is *not* a `sp_ui` preference, so it does
not affect the `no Vary: Cookie` conclusion in `§3.7` — the `<link>` is rendered from the
campaign slug, which is already in the path.

**Tasks**

- [ ] Define the theme manifest schema; validate the brand pair and font paths; emit `/c/{slug}/theme.css` from the manifest.
- [ ] Assert a raw `.css` in a content root is never linked from the shell.
- [ ] Add `theme.brand_invalid` as an error-level event; add the GM notice.
- [ ] Add a fixture campaign theme to the contrast test, and assert every protected variable is absent from the generated stylesheet.

---

## 5. Scaling and density

### 5.1 Scale model

| Token | compact | medium / large | tv | tv-wide |
|---|---|---|---|---|
| `--type-scale` | 1.0625 | 1 | 1.5 | 1.75 |
| `--space-scale` | 1.125 | 1 | 1.5 | 1.75 |
| `--target-min` | 2.75rem (44px) | 2.25rem (36px) | 3.5rem (56px) | 3.5rem |

`html { font-size: 100% }` always. Every size is `rem` derived from a token — never a `px`
font-size, never a fixed `height` on a text container. This is what keeps 1.4.4 and 1.4.12
satisfiable while letting TV mode scale the interface.

Type scale at `--type-scale: 1`:

| Token | rem | px | Use |
|---|---|---|---|
| `--text-xs` | 0.75 | 12 | footer legal line only; never body, never a control label on TV |
| `--text-sm` | 0.875 | 14 | metadata, table headers, badges, kind badges |
| `--text-base` | 1.0625 | 17 | UI chrome |
| `--text-md` | 1.25 | 20 | article prose base, h4, lead |
| `--text-lg` | 1.5 | 24 | h3 |
| `--text-xl` | 1.875 | 30 | h2 |
| `--text-2xl` | 2.25 | 36 | h1 |
| `--text-3xl` | 3 | 48 | display — campaign name on `/play` and TV |

TV floor: nothing below `--text-md` except the footer legal line.

Line heights unitless (1.5 prose, 1.2 headings, 1.5 chrome). Paragraph margins in `em`. No
text container has a fixed height. 1.4.12 compliance depends on all three.

### 5.2 Typeface

| Role | Default stack | Campaign override |
|---|---|---|
| `--font-ui` | `system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif` | `@font-face` **prepended**, system stack retained as fallback |
| `--font-prose` | `ui-serif, Georgia, Cambria, "Times New Roman", Times, serif` | `@font-face` **prepended**, system stack retained as fallback |
| `--font-mono` | `ui-monospace, SFMono-Regular, "Cascadia Mono", Menlo, Consolas, monospace` | **not overridable** — code and dice expressions must stay monospaced |

The two defaults are system stacks: no webfont, no FOUT, no extra bytes, no licence obligation.
Serif prose is what readers expect from a wiki. Plan `§10.1` adds campaign fonts, so a campaign
*may* supply one — but the system stack is never removed, so a font that fails to load degrades
to today's design rather than to a broken one. `font-display: swap` is forced by the server
(`§4.12.2`).

`{{statblock}}` and `{{dice}}` render in `--font-mono` because they are code-like. Plan `§5.4`
emits them as sanitised HTML, so this is styling, not tokenisation.

### 5.3 Prohibited scaling techniques

| Technique | Why banned |
|---|---|
| `transform: scale()` on a shell | breaks hit-testing against visual bounds, blurs text, desynchronises focus rings |
| `px` font-size | fights the user's browser zoom |
| Fixed pixel `height` on a text container | clips at 200% zoom and under 1.4.12 |
| `user-scalable=no` / `maximum-scale=1` | fails 1.4.4 outright |
| Viewport units for type | non-linear scaling breaks 1.4.4; `clamp()` permitted only for the article measure |

**Tasks**

- [ ] Implement `--type-scale` / `--space-scale` as `calc()` multipliers applied per `[data-ui]`.
- [ ] Assert no rule sets a `px` font-size and the viewport meta has no `maximum-scale`.

---

## 6. Theming

### 6.1 Token layers

1. **Primitive** — `oklch` ramps, `--space-*`, `--radius-*`, `--dur-*`. No meaning.
2. **Semantic** — `--bg`, `--surface`, `--text`, `--accent`, `--callout-*`, `--brand-*`, …
   Meaning, no component knowledge.
3. **Component** — `--btn-primary-bg`, `--tab-selected-fg`. The small set that cannot be
   expressed semantically.

Layers 2 and 3 are defined for `[data-theme="light"]` and `[data-theme="dark"]`. **A token
present in only one theme is a bug**, and the contrast test fails on it.

### 6.2 Light theme — "parchment and ink"

| Token | Value | Luminance | Ratio on `--bg` | Floor |
|---|---|---|---|---|
| `--bg` | `#F7F6F3` | 0.9216 | — | — |
| `--surface` | `#FFFFFF` | 1.0000 | 1.08 | — |
| `--surface-sunken` | `#EFEDE8` | 0.8557 | 1.08 | — |
| `--text` | `#16130F` | 0.0067 | **17.13** | 4.5 |
| `--text-muted` | `#4A443C` | 0.0592 | **8.90** | 4.5 |
| `--text-subtle` | `#5F594F` | 0.1015 | **6.42** | 4.5 |
| `--border-subtle` | `#D6D2C9` | 0.6461 | 1.41 | decorative only |
| `--border` | `#8A8377` | 0.2297 | **3.47** | 3.0 |
| `--accent` | `#1B4D8F` | 0.0753 | **7.76** | 4.5 |
| `--accent-solid` | `#1B4D8F` | 0.0753 | 7.76 | 3.0 boundary |
| `--on-accent` | `#FFFFFF` | 1.0000 | **8.38** on `--accent-solid` | 4.5 |
| `--accent-surface` | `#E8EFF8` | 0.8567 | 1.08 | — |
| `--callout-surface` | `#EFEDE8` | 0.8557 | 1.08 | — |
| `--callout-border` | `#8A8377` | 0.2297 | **3.24** on `--callout-surface` | 3.0 |
| `--callout-secret` | `#4A443C` | 0.0592 | **8.30** on `--callout-surface` | 4.5 |
| `--callout-revealed` | `#1B4D8F` | 0.0753 | **7.24** on `--accent-surface` | 4.5 |
| `--success` | `#1E6B3A` | 0.1110 | **6.04** | 4.5 |
| `--warning` | `#8A5A00` | 0.1271 | **5.49** | 4.5 |
| `--danger` | `#A4262C` | 0.0946 | **6.72** | 4.5 |
| `--focus-ring` | `#0A58CA` | 0.1131 | **5.96** | 3.0 |
| `--brand-accent` | *defaults to* `--accent` | — | — | validated `§4.12.3` |
| `--brand-accent-ink` | *defaults to* `--on-accent` | — | — | validated `§4.12.3` |
| `--map-dim` | `rgb(0 0 0 / 0.18)` | — | — | 3:1 via `--map-outline` |

### 6.3 Dark theme — "slate and ember"

| Token | Value | Luminance | Ratio on `--bg` | Floor |
|---|---|---|---|---|
| `--bg` | `#14181D` | 0.0089 | — | — |
| `--surface` | `#1B2027` | 0.0141 | 1.06 | — |
| `--surface-sunken` | `#0E1116` | 0.0055 | 1.03 | — |
| `--text` | `#EDEEF0` | 0.8545 | **15.35** | 4.5 |
| `--text-muted` | `#A8B0BA` | 0.4292 | **8.13** | 4.5 |
| `--text-subtle` | `#8B94A0` | 0.2921 | **5.81** | 4.5 |
| `--border-subtle` | `#2B323B` | 0.0311 | 1.13 | decorative only |
| `--border` | `#6B7684` | 0.1774 | **3.86** | 3.0 |
| `--accent` | `#7FB2F0` | 0.4265 | **8.09** | 4.5 |
| `--accent-solid` | `#5B9BE8` | 0.3149 | **6.19** | 3.0 boundary |
| `--on-accent` | `#0B1220` | 0.0061 | **6.51** on `--accent-solid` | 4.5 |
| `--accent-surface` | `#1D2A3D` | 0.0225 | 1.15 | — |
| `--callout-surface` | `#0E1116` | 0.0055 | 1.03 | — |
| `--callout-border` | `#6B7684` | 0.1774 | **4.10** on `--callout-surface` | 3.0 |
| `--callout-secret` | `#A8B0BA` | 0.4292 | **8.63** on `--callout-surface` | 4.5 |
| `--callout-revealed` | `#7FB2F0` | 0.4265 | **6.57** on `--accent-surface` | 4.5 |
| `--success` | `#5FCB8E` | 0.4709 | **8.84** | 4.5 |
| `--warning` | `#E0B341` | 0.4847 | **9.08** | 4.5 |
| `--danger` | `#F0877F` | 0.3739 | **7.20** | 4.5 |
| `--focus-ring` | `#9CC4FF` | 0.5376 | **9.97** | 3.0 |
| `--brand-accent` | *defaults to* `--accent` | — | — | validated `§4.12.3` |
| `--brand-accent-ink` | *defaults to* `--on-accent` | — | — | validated `§4.12.3` |
| `--map-dim` | `rgb(0 0 0 / 0.32)` | — | — | 3:1 via `--map-outline` |

Every ratio was computed by hand with the WCAG relative-luminance formula and is a **design
target, not a verified value** — `§10.1` makes machine verification a gate.

### 6.4 The dark-mode polarity rule

**Solid fills invert in dark mode.** A `#1B4D8F` button on a `#14181D` page measures 2.13:1
against its own background: its label passes at 8.38:1 and its **shape fails the 3:1 non-text
requirement of 1.4.11**. Dark mode therefore uses a light fill (`--accent-solid: #5B9BE8`)
with a near-black label (`--on-accent`): 6.19:1 against the page, 6.51:1 for the label. Same
for `--danger` and every other filled control. A hard requirement, not a preference.

### 6.5 Semantic guarantees

- **Never colour alone.** Every status pairs hue with an icon, a text label, or both. The
  initiative current-turn marker carries the token's name in a bold weight and a left bar — the
  most-glanced element in the app. The revealed-secret marker is a *text* label (`§4.10.4`).
- **`--warning` is a text/icon colour, never a fill.** Dark amber on white is the classic
  illegible pairing.
- **`--border-subtle` may not be the only boundary of an interactive element.** Control outlines
  use `--border` (≥3:1). This is what 1.4.11 asks for.
- **Focus rings are two-tone** — `--focus-ring` plus a `--focus-ring-offset` matching the local
  surface colour, so the ring survives landing on a same-hue control. Focus on a solid fill
  inverts the ring to `--on-accent`.
- **Protected variables** (`§4.12.1`) are not just "don't set" — the theme generator does not
  emit them, so no campaign value can reach them.

### 6.6 Theme state, `ETag`, and the cache rules

**`sp_ui` carries two fields only:**

| Field | Values | Default |
|---|---|---|
| `theme` | `auto` \| `light` \| `dark` | `auto` |
| `ui` | `auto` \| `laptop` \| `phone` \| `tv` | `auto` |

`SameSite=Lax; Path=/; Max-Age=31536000`, not HttpOnly, no sensitive data. **No density field**
— density was not selected, and a dangling token is worse than no token.

Resolution is entirely client-side before first paint (`§3.7`). **No `Vary: Cookie` is emitted
and none is needed**, because `sp_ui` never varies the document.

**`ETag`. Plan `§5.5` says "`ETag` = the same hash", but the same section defines two body
variants from one `content_hash`. Those cannot share an `ETag` — so this is a defect in the
plan, and the rule this spec requires is:**

```
ETag = W/"<sha256(content_hash + ':' + include_secrets_flag)>"
```

Without the salt, a GM's unredacted response and a player's redacted one would advertise the
same validator, and any cache holding both would serve whichever it stored first — a **direct
secret leak to a player**. This is the UI half of plan `§14`'s "highest-value security test".

**Cache directives:**

| Response | `Cache-Control` | Why |
|---|---|---|
| Wiki page, `include_secrets=false` (player, anonymous) | normal, `ETag`-revalidated | the common case; plan `§5.5`'s shared-cache story is safe here because the body is identical for every reader in this tier |
| Wiki page, `include_secrets=true` (GM) | **`private, no-store`** | must never enter a shared cache under any configuration |
| `/c/{slug}/theme.css` | public, `ETag` | derived from the manifest, not from a viewer |
| `/c/{slug}/assets/…` | private if the campaign is private (plan `§9`) | |

A redacted page is permission-neutral *within its tier*, which is why only the GM variant needs
`no-store`. The cost is that a GM navigating between wiki pages re-fetches — negligible next to
the alternative.

**Tasks**

- [ ] Implement `sp_ui` read/write with exactly the two fields.
- [ ] Salt the `ETag` with the `include_secrets` flag; add a test asserting a GM and a player response never share a validator.
- [ ] Set `private, no-store` on every `include_secrets=true` response; assert in a test.
- [ ] Emit the inline head resolver; assert the served document contains neither `data-theme` nor `data-ui` before script execution.
- [ ] Add the `forced-colors` border fallbacks for every elevation token.

### 6.7 Forced colours, contrast, motion

| Query | Response |
|---|---|
| `forced-colors: active` | our palette does not survive. `forced-color-adjust: auto` throughout; **every shadow-based separation gets a 1px `ButtonBorder`/`CanvasText` fallback**, because shadows are dropped in this mode. Focus indicators switch to `Highlight`. |
| `prefers-contrast: more` | `--text-muted` and `--text-subtle` promote to `--text`; `--border` thickens to 2px. |
| `prefers-reduced-motion: reduce` | all `--dur-*` → 1ms. Map pan and zoom become instant. No auto-advancing content, no parallax, no animated dice. Nothing may depend on an animation to become visible. |

**Tasks**

- [ ] Write `tokens.css` with all three layers, `oklch` primitives and both theme blocks, emitted through Tailwind v4's CSS-first `@theme`.
- [ ] `tokens_contrast_test.go` in `internal/web`: parse the token file, assert every pair in `§6.2`/`§6.3` against its floor, assert no token is missing from a theme, and run the whole set again with the fixture campaign theme applied.

---

## 7. Accessibility contract

**Target: WCAG 2.2 Level AA, with AAA claimed only where stated.**

### 7.1 Contrast

| Content | Requirement | Status |
|---|---|---|
| Body and control text | ≥4.5:1 (1.4.3) | every pair clears it; minimum observed 5.49:1 |
| Large text (≥24px, ≥18.66px bold) | ≥3:1 | ≥4.5:1 taken anyway |
| Component boundaries, focus rings, selected states | ≥3:1 (1.4.11) | `--border`, `--focus-ring`; solids follow `§6.4` |
| Graphics and meaningful images | ≥3:1 (1.1.1) | map outlines, dice pips, the revealed-secret marker |
| Disabled controls | exempt from 1.4.3 | still legible |

### 7.2 Landmarks, structure, skip links

```
header[banner]  nav[navigation][aria-label="Campaign"]  main  aside[complementary]  footer[contentinfo]
```

The compact bottom bar is a second `nav`, so **both must carry distinguishing labels.**
Unlabelled duplicates make landmark navigation useless — a gate failure, not a style nit. A
plugin page type may not add a landmark (`§4.11.1`).

**Skip links**, first focusable elements in the document, in order:

1. "Skip to content" → `#main`
2. "Skip to campaign navigation" → `#nav` (only where the nav exists, `§4.6`)
3. "Skip to utilities" → `#rail` (only where the rail is persistent or reachable)
4. On `/play` only: "Skip to the token list"

Targets are real focusable elements with `tabindex="-1"`, so focus lands somewhere a screen
reader will announce.

Structural rules: exactly one `<h1>`; heading levels never skip; `lang` on `<html>`; `<title>`
follows `Page — Section — Campaign`. **No `world`, no `session` in any rendered string**
(`§1.2`).

### 7.3 Target size

| Input | Minimum | Standard |
|---|---|---|
| Pointer / desktop | 24 × 24 CSS px | WCAG 2.2 SC 2.5.8 (AA) — a floor, not the goal |
| Touch | 44 × 44 CSS px | WCAG 2.5.5 (AAA), Apple HIG 44pt, Material 48dp |
| TV / D-pad | 56px along the movement axis, 40px across, ≥16px gutter | no WCAG criterion covers 10-foot; platform norm |

Enforced by construction: a shared `.target` utility sets
`min-inline-size: var(--target-min); min-block-size: var(--target-min)`, and every interactive
element — **including plugin components** (`§4.11.1`) — uses it. Small-looking controls get
hit-slop padding. ≥8px separation at touch, ≥16px on TV, so a mis-aimed remote press cannot
land on a neighbour.

### 7.4 Keyboard and D-pad

| Key | Behaviour |
|---|---|
| `Tab` / `Shift+Tab` | natural document order. **No positive `tabindex`, anywhere** — gate failure |
| `Enter` / `Space` | activate. Both, on every control |
| `Escape` | close the topmost layer; return focus to its trigger |
| Arrows | within a composite widget (tabs, menu, toolbar, token list) move between items; elsewhere page scroll |
| `/` | focus the search field |
| `?` | open the shortcut sheet |
| `g` then `w` / `s` / `p` | go to wiki / search / play |
| `Home` / `End` | first / last item in a composite widget |
| Remote `OK` / `Back` | map to `Enter` / `Escape` |
| Remote arrows | spatial focus; history within a region; never a scroll trap |

- `:focus-visible` for pointer tiers. **On TV, `:focus` and `:focus-visible` are styled
  identically** — some TV browsers lack `:focus-visible`, and a ring that appears only for
  keyboard input may then never appear at all.
- `outline: none` without a visible replacement is prohibited.
- **Focus is never moved by a background event.** A dice roll, a presence change or a reconnect
  must not steal focus.
- TV auto-dim is permitted **only** for navigation affordances, never for information, never
  with a dialog open, and never within 4s of a focus movement or a dice result. Initiative,
  clock, chat and connection state never auto-hide.

### 7.5 Live regions, and which transport feeds them

The plan contradicts itself on the play page: `§7` says "no SSE on the VTT page" and "SSE
appears only for live game chrome"; `§3.1` lists initiative/chat/presence as Datastar/SSE
fragments; `§9` routes `/c/{slug}/events` as "SSE (editors + live chrome)"; and `§7.1`'s
WebSocket protocol also carries `presence`. **Resolution:**

| Channel | Carries |
|---|---|
| **WebSocket** `/c/{slug}/ws` | client → server: intents, presence. server → client: ordered state — placement deltas, rolls, clock, snapshot, `applied`/`rejected` — consumed by PixiJS as structured data |
| **SSE** `/c/{slug}/events` | server → client: **rendered DOM fragments** for the sidebar — initiative, chat, dice log, connection and degraded notices — patched by Datastar. Plus the editor's external-change notice on `/edit` |

One hub, two egress representations of the same state: the canvas needs structured deltas, the
sidebar needs HTML. Two connections on the play page, against plan `§7`'s ~6 ceiling. `§7`'s
"no SSE on the VTT page" is read as **no SSE for map state**. Presence rides the WebSocket,
because the plan's `§7.1` protocol already defines it there and it needs no versioning.

| Announced content | Channel | Region |
|---|---|---|
| Dice result, HP change, turn change, chat line, delta applied | SSE fragment → sidebar | `role="status"`, `aria-live="polite"` |
| Presence join/leave | WebSocket | `role="status"`, polite |
| Presence cursors | WebSocket | **no live region** — visual only, `aria-hidden` |
| Map-side state (placements moved, camera) | WebSocket → canvas | no live region; the token list is the accessible equivalent (`§7.6`) |
| Editor external-change notice | SSE | `role="status"`, polite |
| Editor save state | HTTP response | `role="status"`, polite (`§4.8`) |
| **`412` conflict on save or reveal** | HTTP response | `role="alert"`, `aria-live="assertive"` |
| Secret reveal, reconcile-capped | SSE fragment / status page | `role="alert"`, assertive (`§4.10.3`) |
| Ruleset drift, game ended, connection lost | WebSocket | `role="alert"`, assertive (`§4.7`) |

**Patches must never touch the focused element**, and insertions into a live region are
throttled to 1/second. The chat and dice logs must not replay history into a live region on
connect — a user joining mid-table would otherwise hear the whole table read aloud.

**Search has no live region, because search is not live.** Type-ahead is content delivery, which
plan `§3.1` confines to two SSE uses, and a result list over FTS is exactly the cacheable,
session-stable content the plan says never flows through a reactive transport. Search is
**submit-to-navigate** over ordinary HTTP, `GET /c/{slug}/search?q=`, `ETag`-cacheable. The
count lives in the `<h1>` ("12 results for *goblin*"). A client-side search renderer is
prohibited: it could not reproduce the server's FTS tokenisation (plan `§4.3`).

**Tasks**

- [ ] Assert no `aria-live` region exists on the search route; assert the response carries an `ETag`.
- [ ] Assert the play page opens exactly one WS and one SSE.

### 7.6 The map canvas accessibility contract

The PixiJS canvas is `aria-hidden="true"` and is a **decorative mirror**. The accessible
representation is real DOM:

- The rail's **Tokens** tab is a `<ul>` of `<button>` elements, one per placement, each naming
  it, its hit points, its conditions and its layer. This list is the accessibility source of
  truth; the canvas is the visual one.
- `ArrowUp`/`ArrowDown` move focus between placements; `Enter` selects and moves the camera;
  selection mirrors to the canvas.
- On TV the token list is the **primary** way to change what the map shows, because dragging is
  unavailable. On a phone the whole map is a readout (`§4.9`) and the list is the only
  representation — which makes this component, not the canvas, the load-bearing one.
- A **map contrast overlay** is a first-class control: `--map-dim` on the map layer, 3px
  `--map-outline` on every placement and grid line. Much VTT artwork is low-contrast by
  design; without this the map fails 1.4.11 in practice.
- `role="application"` is **prohibited**.
- Map labels inside the canvas scale with `--type-scale`.
- **Camera rule on resize:** zoom is expressed in world coordinate units, never pixels. The
  client fits the world bounds to the viewport and preserves the world centre across resize and
  orientation change. Without this the map re-frames on every breakpoint crossing — which at
  the 14" target happens constantly.

### 7.7 Forms, dialogs, menus

- Labels are `<label for>`, never placeholders.
- Errors: a summary at the top of the form linking to each field, plus `aria-invalid` and
  `aria-describedby`. Focus the first error.
- Dialogs: `role="dialog"`, `aria-modal="true"`, labelled by their heading, focus trapped,
  `Escape` closes, focus returns to the trigger.
- The `412` conflict diff **replaces the editor's preview pane** at ≥1024 and is a full-height
  layer above the Edit/Preview tabs below that. Never a modal: the editor is already a split
  view, so a modal hides one side of the comparison (`§4.8`).
- Menus use the WAI-ARIA menu-button pattern. **On touch and TV a menu is a full-width bottom
  sheet**, not a popover anchored to a 44px trigger.

### 7.8 Tables

- Row height ≥44px at touch, ≥36px desktop. Header row sticky within the centre's scroll region.
- Sort is a `<button>` inside `<th scope="col">` with `aria-sort`.
- Row actions live in an overflow menu cell, not four always-visible icon buttons — 80 tab stops
  destroys usability.
- **Below 640px every table becomes a stacked card form**: each row an `<article>` with `<dl>`
  pairs and its own overflow menu. Required by 1.4.10 and the only thing that works at 360px.

### 7.9 Reflow, resize, orientation

- **320 × 256 CSS px with no two-axis scrolling** (1.4.10).
- 200% zoom on a 1366px laptop gives a 683px viewport and correctly drops a tier. Nothing is
  lost; the editor buffer survives.
- 400% zoom equivalent (320px) reaches everything.
- 1.4.12 overrides clip nothing.
- Orientation change preserves scroll position, focus and unsaved state.

### 7.10 Prohibited

`role="application"` · positive `tabindex` · `outline: none` without replacement ·
`user-scalable=no` · colour as the sole carrier of state · hover-only affordances · unpausable
autoplaying motion · target sizes below `§7.3` · `aria-hidden` on a focusable element · a live
region firing more than once per second · `:has()` or container queries for anything
load-bearing on a TV · a plugin component exempt from any of the above.

**Tasks**

- [ ] Build the primitive library to this contract: dialog, menu-button, tabs, disclosure, bottom sheet, skip link, target utility, two-tone focus ring.
- [ ] Implement the token list as real DOM; assert the canvas carries `aria-hidden="true"` and the list is keyboard-operable end to end.
- [ ] Add the `prefers-reduced-motion` and `forced-colors` blocks to every component.
- [ ] Add the structural a11y test and make it gate-blocking.
- [ ] Implement the table component with the <640px stacked form.

---

## 8. Component hierarchy across input methods

### 8.1 Four tiers

| Tier | Contents | May collapse? | May be sticky? | Owns focus? |
|---|---|---|---|---|
| **1 — Chrome** | header, footer, left nav | never — both required to persist | yes | yes, on open |
| **2 — Rails** | left nav contents, right utility panels | yes, to an icon rail; sheets/drawers below 1024 | yes | yes, when a sheet or drawer |
| **3 — Content surface** | page, map, editor, result list, tables, plugin page types | no | the page outline only | no |
| **4 — Transient** | dialogs, sheets, menus, toasts, tooltips | n/a | sheets only | yes, exclusively |

Tier 4 is modal and traps focus; nothing in tiers 1–3 is modal. A transient layer never obscures
a tier-1 element on touch, and on TV never obscures focus. Focus order is 1 → 2 → 3 → 4, and
tier 4 returns focus on close.

### 8.2 Input-modality matrix

| Behaviour | Fine pointer | Coarse pointer | TV |
|---|---|---|---|
| Primary affordance | visible label | persistent labelled control, ≥44px | ≥56px along the movement axis |
| Secondary actions | hover-revealed **and** keyboard-reachable | always visible; no hover | always visible; no hover |
| Context menu | right-click, with a keyboard equivalent in the overflow menu | long-press → bottom sheet | **overflow menu tile**, not long-press |
| Tooltips | hover + focus | tap-to-toggle via `aria-describedby` | **absent** — no hover, no hover text |
| Focus ring | 2px + 2px offset | 2px + 2px offset | 4px + 4px offset, never suppressed |
| Mode switcher | header menu | "More" sheet | first tile on the home grid |
| **Text entry** | full | full | **none** |
| Selection | click, shift-click, marquee | tap, long-press | single focus, `Enter` selects |
| Scroll | wheel, trackpad, scrollbar | momentum | D-pad, focused element scrolled into view |

The load-bearing row is **text entry**. There is no typing on a TV remote, so any control
requiring text is unreachable there *by construction*. On TV every such control needs a
non-typing equivalent — a picker, a chip, or a tile grid. The search field, the Rules tab's
module config, and `/c/{slug}/settings` all need one.

### 8.3 Visual hierarchy

| Rank | Element | Treatment |
|---|---|---|
| 1 | Current location (h1, campaign name) | `--text-2xl`; `--text-3xl` on `/play` and TV |
| 2 | Primary action per view | exactly one solid `--accent-solid` per panel; never two competing |
| 3 | Section headings (h2) | `--text-xl`, 1px `--border-subtle` rule beneath |
| 4 | Body and lists | `--text-md` prose, `--text-base` chrome, `--text-muted` metadata |
| 5 | Utility and metadata | `--text-sm`, `--text-subtle` |
| 6 | Decoration | `--border-subtle`, elevation. Never the only boundary of anything interactive. |

Prominence follows the input method, not the tier:

- **TV**: ranks 1–2 grow by `--type-scale`; ranks 4–5 do not shrink below `--text-md`. Absolute
  floor.
- **Touch**: prominence shifts *down* to rank 2, because the primary action moves to the bottom
  of the reach envelope.
- **Ultrawide**: rank 4 does not grow. Extra width becomes gutter.

**Tasks**

- [ ] Add a `data-modality` hook (`fine` / `coarse` / `tv`) set by the same inline script; drive `§8.2` from it.
- [ ] Build the TV non-typing equivalents: campaign picker as a tile grid, search as a chip-driven result list, Rules tab module toggles as tiles.
- [ ] Assert the compact bottom bar contains the primary action for every route.

---

## 9. Failure modes and degradation

| Condition | Behaviour |
|---|---|
| Old TV browser without WebGL2 | map falls back to the static image layer; a notice replaces the canvas; **all chrome and the token list keep working** |
| TV browser without `:focus-visible` | `§7.4` — TV mode styles `:focus` identically |
| TV browser without `dvh` | `vh` fallback precedes `dvh` |
| TV browser without container queries / `:has()` / `subgrid` | progressive enhancement only; the shell uses explicit `grid-template-areas` |
| 200% zoom on the 14" laptop | drops to a narrower tier, nothing lost |
| Ultrawide 3440px | centre capped by the measure; extra space is gutter |
| `forced-colors: active` | `§6.7`; every shadow separation has a border fallback |
| Sheet open on touch, then rotate to short-landscape | sheet closes and returns focus to its trigger |
| WebSocket reconnect during a dialog | no focus change, no toast over the dialog; the header indicator is the only signal |
| TV auto-dim while a dice result lands | suppressed for 4s |
| `prefers-reduced-motion` on `/play` | instant pan/zoom, no animated dice; state announced, not animated |
| SSE reconnect on the editor | notice re-arms; the unsaved buffer is never discarded automatically |
| **Campaign brand pair fails contrast** | last known-good theme retained, `theme.brand_invalid` logged at error level, GM notice (`§4.12.3`) |
| **Campaign theme manifest unparseable** | same as above; the core theme stands. The manifest is attacker-reachable, so it is parsed with the same limits as front matter (plan `§12`) |
| **Gameplay plugin missing** (`system_id` unknown) | wiki serves 200; the Table link is **absent**; the campaign overview carries a persistent notice (`§4.7`) |
| **`ruleset_version` mismatch** | `/play` refuses to resume with an `role="alert"` block; default is refuse (plan `§16.5`); wiki unaffected |
| **Secret reconcile budget exhausted** | the secret stays hidden; `secret.reconcile_capped` at error level; GM alert on `/play` and a row on `/c/{slug}/status` |

**Tasks**

- [ ] Add the WebGL2 capability check with the static-image fallback.
- [ ] Add the auto-dim suppression triggers.
- [ ] Add the two plugin-degraded states from `§4.7` to the route surface test.

---

## 10. Validation

There is no Node in this repo, so **CI-grade automation is Go `httptest` over rendered shell
HTML.** The full audit pass is agent-assisted via the Playwright MCP, which per
`.kilo/skills/browser-e2e` must not become a CI dependency. `make check` must be green.

### 10.1 Contrast — automated, gate-blocking

Parse the token file in a Go test; assert every pair in `§6.2`/`§6.3` against its floor; assert
no token is missing from either theme; then re-run the whole set **with the fixture campaign
theme applied**, since a brand override is the one user-supplied value that reaches a colour
pair. This replaces the hand-computed ratios above.

### 10.2 Structural a11y — automated, gate-blocking

For every route, including every registered plugin page type: exactly one `<h1>`; landmarks
present with distinguishing labels; no `tabindex` > 0; no `outline: none` without a
replacement; no `aria-hidden` on a focusable element; skip links first in tab order; every
`data-testid` present; **and no rendered string contains "world" or "session"** (`§1.2`).

### 10.3 Viewport and orientation sweep — agent-assisted

All twelve rows of `§2`, plus 200% zoom at 1366, plus 360 × 640 both orientations. No
horizontal scrollbar at 320px, nothing clipped, the bottom bar never covering content, no sheet
leaving under 40px of context.

### 10.4 Keyboard-only walkthrough — agent-assisted

Every route, mouse untouched: tab order matches DOM order; every action reachable; no trap
outside dialogs; focus visible at every stop; focus returns to the trigger on close.

### 10.5 D-pad walkthrough (TV) — agent-assisted

Every route with a simulated remote: documented focus order; no focus loss on reconnect; no
hover-only affordance; every typed-input control has a non-typing equivalent; target sizes met
with 16px gutters.

### 10.6 Target size audit — automated

Assert `.target` minimums hold at 320px; grep rendered markup for interactive elements missing
the class, **including plugin output**.

### 10.7 Media-query matrix — agent-assisted

`prefers-reduced-motion`, `prefers-contrast: more`, `forced-colors: active`,
`prefers-color-scheme` both values, both themes, two viewports. Sixteen combinations.

### 10.8 Route-specific checks

| Surface | Automated | Agent-assisted |
|---|---|---|
| Editor (`§4.8`) | textarea has a `<label>`; toolbar is `role="toolbar"`; preview is `aria-live="off"`; save state is `role="status"`; buffer survives a `412` | keyboard-only edit and save; undo survives a toolbar action; split at 1024, tabs at 375 |
| Phone play (`§4.9`) | compact canvas is `aria-hidden` with no pointer handler; action bar replaces nav on `/play` only; the rail sheet never covers the action bar at 320 × 568 | thumb reach for all four controls; no gesture conflict; a read-only map is not mistaken for a broken one |
| Search (`§7.5`) | count in the `<h1>`; no live region on the route; response carries an `ETag` | no type-ahead, no client fetch on keystroke |
| **Secrets (`§4.10`)** | **the secret text appears in no non-GM response** — not the DOM, not the accessibility tree, not a `data-` attribute, not a JSON payload (plan `§14`'s highest-value test, UI half). No reveal control in any non-GM response. GM and player `ETag`s differ. | a sighted player is not shown a stub unless the campaign opted in; the gap warning fires only mid-paragraph |
| **Theme layer (`§4.12`)** | generated `/c/{slug}/theme.css` contains no protected variable; a raw `.css` in a content root is never linked; an invalid brand pair keeps the last good theme and logs `theme.brand_invalid` | a campaign theme is visibly applied and the UI stays readable |
| **Plugin UI (`§4.11`)** | the structural a11y test passes for every registered plugin page type; no hard-coded kind label in the shell; a client-side roll result is never rendered | a plugin component inherits `.target`, tokens and the focus ring |

### 10.9 What a browser pass must become

Every finding from 10.3–10.8 becomes a committed Go test. MCP-driven steps never become CI
steps.

**Tasks**

- [ ] `internal/web/tokens_contrast_test.go` — `§10.1`, with the fixture campaign theme.
- [ ] `internal/httpapi/shell_render_test.go` — `§10.2`, `§10.6`, every route in `§4.4`.
- [ ] `internal/httpapi/route_surface_test.go` — `§10.8`, one file per surface so a blocked slice fails only its own tests.
- [ ] `internal/httpapi/secret_render_test.go` — the plan's highest-value test, UI half.
- [ ] Add `make a11y` running all of them, and include it in `check`.

---

## 11. Risks

| Risk | Impact | Mitigation |
|---|---|---|
| Old TV browsers are an open-ended target | `dvh`, `:focus-visible`, container queries, WebGL2 all vary | explicit fallbacks (`§9`); no load-bearing `:has()`/`subgrid`/container queries |
| Contrast values are hand-computed | a typo ships an AA failure | `§10.1` gate fails the build |
| Five tiers plus a TV mode plus two theme layers is a lot of surface | tiers drift apart over time | one root attribute pair, one canonical DOM, tiers are CSS only, and the theme layer cannot reach a protected variable because the server never emits one |
| The `ETag` salt is easy to get wrong | a GM's unredacted body cached and served to a player | `§6.6` makes it an explicit test in `§10.2` and `§10.8`, not a code-review note |
| A campaign supplies a font that fails to load | falls back to the system stack | system stack is never removed (`§5.2`); `font-display: swap` forced |
| The 580px centre height is unforgiving | vertical overflow on a dialog | every dialog is height-capped and internally scrollable; test at 768px tall specifically |
| D-pad focus order is undocumented in practice | unusable TVs discovered late | `§10.5` produces a written focus order per route as a deliverable |
| Left rail assumed to hold <10 campaigns | unusable nav at scale | acknowledged; group-and-filter deferred (`§14.4`). A TV filter is also blocked by "no text entry" |
| The editor preview costs a round trip per pause | lag on a slow LAN | 800ms debounce plus on-demand; a client renderer would be wrong, not merely different |
| No positional play from a phone (`§4.9`) | a table player may expect to move their own token | stated, not hidden. Worth a play-test before it is locked |
| The editor buffer is the user's unsaved work | a bug that clears it loses data | survives orientation, resize, tab switch and `412`; never discarded without a confirmation naming the loss |

---

## 12. Data flow

Read-only for this spec; it consumes the plan's flows.

```
request → middleware chain → route handler
  → resolve access tier (plan §8)
  → content pipeline (plan §5) for the centre slot, with include_secrets
  → build domain.PageLayout{Centre, Rail} + domain.ShellPrefs
  → templ renders ONE canonical shell:
      no data-theme, no data-ui
      ETag = sha256(content_hash + include_secrets)          (§6.6)
      Cache-Control: private, no-store   when include_secrets
      <link href="/c/{slug}/theme.css"> when the campaign has a manifest (§4.12)
  → inline head script sets data-theme + data-ui from sp_ui / ?ui= / UA hint / media queries
  → CSS selects tier, density, theme, campaign brand; zero further round trips

live: WS in (intents, presence) → hub applies → state
        ├→ WS out: ordered deltas → PixiJS (structured)
        └→ SSE out: rendered sidebar fragments → Datastar → DOM (§7.5)
```

Three invariants:

- **The document never varies by `sp_ui`.** No `Vary: Cookie`, and plan `§5.5`'s
  content-hash invalidation signal survives intact.
- **The document varies by access tier and by `include_secrets`**, and the second is a security
  boundary (`§6.6`).
- **The shell opens no SSE or WebSocket.** The play page opens exactly one of each (§7.5); the
  editor opens one SSE. Plan `§7`'s connection ceiling is never approached by chrome alone.

---

## 13. Rollout

Nothing to migrate, no stored preference to backfill, no schema change. `sp_ui` is a new cookie
with a safe default for absent readers. Two ordering constraints:

1. The shell must land before any route fills a slot, so no route ships a hard-coded three-column
   layout that bypasses the contract. `§15.2` enforces this.
2. `/c/{slug}/theme.css` must exist before any campaign manifest is accepted, or a campaign
   registration would reference a 404 stylesheet.

---

## 14. Open questions

1. **Should the "Revealed" marker be on by default on the published page?** `§4.10.4` splits it:
   on in the editor, off in play. A GM debugging a reveal wants it; a table mid-scene does not.
   Worth confirming with a real table.
2. **Should the brand layer be per-campaign or per-user?** `§4.12.4` chose per-campaign, which
   keeps it out of `sp_ui` and preserves the no-`Vary: Cookie` result. Per-user would be nicer
   for a shared instance but reintroduces the cookie-varying-document problem.
3. **Default `/play` rail tab per form factor.** `Initiative` on laptop, `Tokens` on TV and on
   a phone, where the map is a readout. Needs a play-test.
4. **Left-rail scale.** Assumes fewer than ten campaigns. Past that it needs grouping and a
   filter — and the filter is unusable on TV.
5. **Whether any native client is ever wanted.** This spec assumes not. If that changes, the
   token layer in `§6` is the only part that ports, which is why it is CSS custom properties
   rather than baked Tailwind theme values.
6. **Print.** Deferred deliberately (`§1.4`). A GM printing a statblock is real and deserves its
   own spec.
7. **The plan's own open questions that reach the UI:** cross-campaign wikilinks
   (`§16.1`), ruleset-drift policy (`§16.5` — default refuse, and `§4.7` designs for that
   default), and whether `page_revisions` needs a UI beyond the rail's revision list.

---

## 15. Build order

### 15.1 Where this spec attaches to the plan

| This spec | Plan phase |
|---|---|
| `§4.1`–`§4.7` shell, `§3`, `§5`, `§6`, `§7` | plan phase 5 (wiki surface) — but see `§15.2` |
| `§4.8` editor | plan phase 5 |
| `§4.10` secrets | plan phase 11 |
| `§4.11` plugin UI contract | plan phases 7–8 |
| `§4.12` theme layer | plan phase 10 |
| `§4.9` phone play | plan phases 6 and 10 |

### 15.2 A defect in the plan's ordering

Plan `§15` builds the templ shell at phase 5 but places "Tailwind standalone build" at phase
10. **A shell cannot be styled before the CSS toolchain exists.** The toolchain steps must move
before phase 5. The smallest fix: split plan phase 5, or move the Tailwind/templ setup into plan
phase 1's "Foundations". This spec's Slice A below is that work, and it should be sequenced
ahead of plan phase 5.

### 15.3 Slice A — the shell. Unblocked

Depends only on the repo as it stands. This is the deliverable of this plan.

1. Toolchain: vendored Tailwind v4 CLI, pinned templ, `make css` / `make templ`, both wired
   into `make check`.
2. Token layer — primitives, both themes, scale multipliers, callout and brand tokens
   (`§5`, `§6.1`–`§6.4`), plus the contrast test (`§10.1`).
3. Shell grid and the two-column pre-campaign variant (`§4`, `§3.2`).
4. Inline head resolver and `sp_ui` (`§3.7`, `§6.6`).
5. Header, footer, left nav (`§4.1`–`§4.3`).
6. Right rail: persistent, drawer, and the below-640px bottom sheet (`§4.5`, `§3.1`).
7. Primitive library: dialog, menu-button, tabs, disclosure, skip link, target utility (`§7.7`).
8. The eleven empty/error/degraded states (`§4.7`).
9. Table component with the stacked form (`§7.8`).
10. Structural a11y test and `make a11y` (`§10.2`, `§10.6`).
11. TV mode: layout, scale, focus rules, home tile grid, non-typing equivalents (`§3.3`,
    `§3.4`, `§8.2`).
12. Agent-assisted sweeps `§10.3`–`§10.7`; convert every finding into a Go test.

Each step leaves the shell usable. A stub route filling each slot in `§4.4` is enough to make
every step demonstrable; no route may hard-code its own three-column layout.

### 15.4 Slice B — route surfaces. Blocked on the content pipeline

The editor (`§4.8`), the `412` diff, the render endpoint, and the theme generator (`§4.12`) all
require goldmark, front-matter parsing and sanitisation to exist. Their *markup* can be built in
Slice A against fixture HTML, so the accessibility contract is proven before the pipeline lands;
the wiring cannot.

### 15.5 Slice C — live play. Blocked on the realtime plan

The `/play` rail tabs, the token list, the map canvas, the WebSocket live regions, the
sidebar-fragment SSE and the phone action bar (`§4.9`) require the hub and the `intent` op set.
The *layout* — two-column grid, tabbed rail, action bar geometry, read-only canvas — can be
built in Slice A against a fixture snapshot, which is the only way to validate the 320 × 580
budget before a hub exists.

### 15.6 Slice D — secrets. Blocked on plan phase 11

`§4.10` needs the two-variant render, the `include_secrets` cache key, the reveal endpoint and
the ledger. Its *markup* — the reveal controls, the stub, the gap warning, the callout tokens —
can be built in Slice A against fixture content, and the contrast test already covers the
callout tokens. The reveal round trip cannot.

**This ordering is why `§4.10`–`§4.12` are specified now.** Every blocked surface is designed
against its block, so the moment the pipeline, hub and ledger land there is nothing left to
decide — only markup to write.
