# semiplane — GitHub Repository Layout: Templates, Labels, CI, Pages

**Status:** implemented. **Date:** 2026-09-30.
**Repo state:** `PopinjayJohn/semiplane`, public, MIT, default branch `main`, no `.github/`
directory at all, zero issues, two committed design plans under `.kilo/plans/`, `make ci`
exists locally but nothing runs it.
**Companions:** `.kilo/plans/1790774477695-semiplane-architecture-overview.md` (owns the
product model and route semantics) and `.kilo/plans/1790778908232-responsive-ui-ux-design-spec.md`
(owns the design-token system and the validation discipline). This plan owns GitHub
plumbing only and cites both rather than restating them.

---

## 1. Scope

**In scope:** `.github/` layout, three issue forms plus a chooser config, one PR template, a
label manifest plus a reconciliation script, a CI gate, a Dependabot config, a security
policy, and a Hugo documentation site on GitHub Pages.

**Out of scope, deliberately:** release/tag workflows (deferred; no releases exist yet),
`CODEOWNERS` and `FUNDING.yml` (noise for a single-maintainer repo), org-level issue types
(an org feature — unavailable on a personal repo), a static sample campaign, site search,
comments, analytics, non-English content, and a Dependabot `docker` entry (no Dockerfile
exists; add it with the file).

---

## 2. Decisions

| Concern | Choice | Why |
|---|---|---|
| Issue templates | YAML issue **forms** | Required fields, dropdowns, per-form auto-labels. Markdown templates are ignored-by-choice in most repos; forms are not ignored, they are skipped. |
| Form order | `01-`, `02-`, `03-` filename prefixes | GitHub sorts templates alphanumerically; the prefix is the only ordering control. |
| PR template | One `.github/PULL_REQUEST_TEMPLATE.md` | Forms do not exist for PRs. Multiple PR templates add a chooser for no benefit at this size. |
| Labels | Manifest in `.github/labels.yml` + `gh`-driven reconcile script | **Labels cannot be declared in-repo.** The API is the only source. A manifest makes them reviewable and diffable. |
| Label axes | `type` / `area` / `status` / flags | Enables compound triage queries. No `priority:` — see §4.2. |
| Site generator | Hugo **standard** edition, no theme, own layouts | `CGO_ENABLED=0 go install github.com/gohugoio/hugo@v0.167.0` drops into the existing `make tools` pattern. No Node, no Ruby, no theme supply chain. |
| Site deploy | Actions → `upload-pages-artifact` → `deploy-pages` | The canonical Pages flow. Branch-based publishing is deprecated. |
| Design plans | Included at build time under `/design/`, bannered as records | Zero duplication, zero drift. They are already public in the repo. |
| Language | English, but content under `content/en/` | Retrofitting i18n later is a config change, not a file move. |
| Tooling versions | Single source in the `Makefile`; CI installs from it | The CI gate and the local gate must be the same tool versions, not two sets of pins. |

### 2.1 Two constraints that shape the whole plan

1. **Labels must exist before the forms reference them.** GitHub silently drops a form's
   `labels:` entries whose labels are absent from the repo. A form committed before
   `make labels` has run produces issues with no `type:` and no `status:`, silently.
   `make labels` is therefore step 1, and `make labels-check` in CI is the guard.
2. **A workflow cannot call a form's `type:` key.** It is org-scoped. A personal repo gets
   structure through the `labels:` key and a `labels-check` job instead.

---

## 3. Target layout

```
.github/
├── ISSUE_TEMPLATE/
│   ├── config.yml              # chooser: blank issues off, 2 contact links
│   ├── 01-bug.yml
│   ├── 02-feature.yml
│   └── 03-question.yml
├── PULL_REQUEST_TEMPLATE.md
├── workflows/
│   ├── ci.yml                  # gate + labels-check + docs-build
│   └── pages.yml               # build + deploy on push to main
├── dependabot.yml
├── labels.yml                  # manifest — labels-as-code
├── SECURITY.md
└── (no CODEOWNERS — see §1)

docs/                           # Hugo site root
├── hugo.toml
├── content/en/
│   ├── _index.md               # landing
│   ├── install/_index.md       # self-host quickstart
│   ├── concepts/_index.md      # campaign / page / kind / placement / ruleset
│   ├── concepts/{content-model,secrets,plugins}.md
│   ├── operating/{_index,security}.md
│   ├── contributing.md
│   ├── roadmap.md              # from architecture §15
│   └── design/
│       ├── _index.md
│       ├── architecture.md     # shortcode, includes .kilo/plans/…-architecture-overview.md
│       └── ui-ux.md            # shortcode, includes .kilo/plans/…-responsive-ui-ux-design-spec.md
├── layouts/
│   ├── _default/{baseof,single,list}.html
│   ├── index.html
│   ├── partials/{head,header,footer,nav,toc,pager}.html
│   └── shortcodes/design-record.html
├── assets/css/tokens.css       # the app's token layer, verbatim, once it exists
├── static/{favicon.svg,og.png,robots.txt}
└── public/                     # build output — gitignore

scripts/sync-labels.sh          # apply | check
```

Rules that make this layout hold together:

- **`docs/` is a self-contained Hugo project root.** Running `hugo` inside `docs/` needs no
  flags and no non-default layout/content/publish paths. The Makefile is the only thing that
  knows where it lives.
- **Nothing in `.github/workflows/` contains a version pin that also appears in the
  Makefile.** Tool versions live in `Makefile` variables; workflows read them from `env`.
- **A `docs/` page that states a behavioural claim links to the plan section or the source
  file that owns it.** The alternative is a site that quietly contradicts shipped behaviour.

---

## 4. Labels

### 4.1 Mechanism

GitHub has no in-repo label declaration. The manifest is the reviewable artifact; the script
is the writer.

```yaml
# .github/labels.yml
- name: type: bug
  color: d73a4a
  description: Behaviour contradicts a documented design. See /design/ for what is documented.
```

`scripts/sync-labels.sh` takes `apply` or `check`:

- `apply` — for each entry, `gh label create --force` (idempotent: creates if absent, updates
  colour and description if present). Never deletes. Prints what changed.
- `check` — diffs the manifest against `gh label list --json name,color,description`, exits
  non-zero on any missing label or any colour/description mismatch, and separately warns
  (without failing) on labels that exist in the repo but not in the manifest, so hand-made
  labels are visible without being fatal.

Requires `jq` and an authenticated `gh` (`GH_TOKEN` in CI, the local login otherwise).
`--repo` is passed explicitly so the script never depends on cwd. Exit non-zero on any
`gh` failure rather than continuing — a half-applied label set is worse than none.

Makefile targets: `make labels` (apply), `make labels-check` (check), both `##`-documented
so they appear in `make help`.

### 4.2 Axes and their rules

| Axis | Cardinality | Applied by |
|---|---|---|
| `type:` | **exactly one** | the issue form's `labels:` key |
| `area:` | at most one | maintainer, from the form's area dropdown |
| `status:` | at most one, and never both a live and a closed-out value | maintainer |
| flags | zero or more, orthogonal | maintainer |

**There is no `priority:` axis.** A priority label without a published triage SLA and a
regularly-used ordering is decoration that rots into noise within a month, and a stale
`priority:P0` sitting on a two-year-old issue is actively misleading.

### 4.3 Label set

**`type:`** — one per issue

| Label | Meaning |
|---|---|
| `type: bug` | Behaviour contradicts a documented design |
| `type: feature` | New capability |
| `type: docs` | Documentation only |
| `type: question` | Needs an answer, not a change. Routed to Discussions |
| `type: chore` | Build, CI, dependencies, housekeeping |

**`area:`** — the product surface the reporter was touching. Deliberately *not* a mirror of
`internal/`; a reporter knows what they were doing, not which package owns it.

| Label | Covers |
|---|---|
| `area: wiki` | Page render, navigation, broken links, search |
| `area: editor` | Web editor, 412 conflict, revisions |
| `area: vtt` | Realtime hub, WebSocket protocol, placements, initiative, map |
| `area: content` | Markdown pipeline, front matter, watcher, render cache, wikilinks, embeds, assets |
| `area: secrets` | `[!secret]` callouts, reveal ledger, redaction |
| `area: auth` | Users, sessions, campaigns, membership, roles, admin |
| `area: store` | SQLite, migrations, FTS5, backup |
| `area: plugin` | `System` interface, data packs, overlays, house rules, UI plugins |
| `area: a11y` | WCAG conformance, keyboard, screen reader, TV mode |
| `area: ops` | Deployment, reverse proxy, binary, config, upgrade path |
| `area: ci` | Actions, Makefile, dependencies |
| `area: website` | This Pages site, docs, issue forms, labels |

**`status:`** — the lifecycle

| Label | Meaning |
|---|---|
| `status: needs-triage` | Default on every new issue. Removed when the issue is understood |
| `status: ready` | Reproduced or understood; actionable as written |
| `status: in-progress` | A branch or PR is open against it |
| `status: blocked` | Waiting on something external; name what in the issue |
| `status: needs-design` | Needs a plan decision before it can be built |
| `status: wontfix` | Decided against. Say why in the issue — this is the label people most often want explained |
| `duplicate` | Not the only one. Always link the canonical issue in the body |

**Flags** — orthogonal booleans

| Label | Meaning |
|---|---|
| `breaking` | Requires a forward-only migration, a ruleset bump, or a URL/protocol change |
| `security` | Security-relevant. Triage privately; see `.github/SECURITY.md` |
| `good-first-issue` | Small, scoped, needs no design decision |

**Colour rule** (applied while filling the manifest): one hue family per axis — `type:`
saturated, `area:` muted, `status:` amber/violet, flags grey/red. Every colour must keep
white label text readable, i.e. low enough luminance; a mid-tone that forces black text
breaks the visual consistency of the sidebar. Illustrative entries are given in §4.1; fill
the rest following the rule rather than inventing a palette per label.

---

## 5. Issue forms

All three set `title:` with a lowercase bracket prefix, so a list of issue titles alone stays
readable and a prefix search still works. Every form body opens with a `markdown` block
pointing at the docs site, not at a wiki page nobody maintains.

### 5.1 `01-bug.yml`

`name: Bug report` (the `name` must exceed three characters or the template is hidden).
`title: "[bug] "`. `labels: ["type: bug", "status: needs-triage"]`.

Body, in order:

1. `markdown` — search existing issues first; if the problem is markdown in your own
   campaign vault, use the campaign-content contact link instead.
2. `input` **version** (required) — release version or commit SHA.
3. `input` **os** (required) — e.g. `linux/amd64`, `darwin/arm64`.
4. `dropdown` **area** (required) — the twelve `area:` labels under human-readable names.
   Its `description` must say plainly: *this does not label the issue; the maintainer
   applies the `area:` label during triage.* A reporter who believes a dropdown labelled
   their issue is worse than one who does not.
5. `textarea` **what-happened** (required) — observed behaviour, including the role you were
   (GM / player / anonymous) and the campaign visibility.
6. `textarea` **expected** (required).
7. `textarea` **steps-to-reproduce** (required) — and a note to wrap `[[wikilink]]`,
   `{{statblock}}`, `{{dice}}` and `[!secret]` in backticks, because unescaped callout
   markers in an issue body trigger GitHub's own alert noise.
8. `textarea` **logs** (optional) — with an explicit instruction not to paste campaign
   content or secret callout text. Logs are where that leaks.
9. `checkboxes` **preflight** — "I searched existing issues and this is not a duplicate"
   (required); "This needs private handling" (optional → `security`).

### 5.2 `02-feature.yml`

`title: "[feature] "`. `labels: ["type: feature", "status: needs-triage"]`.

1. `markdown` — the problem, not the solution. Check `/roadmap` and `/design/` first.
2. `textarea` **problem** (required) — the situation, without proposing a fix.
3. `textarea` **desired-outcome** (required) — what should be possible when it is done.
4. `dropdown` **scope** (required) — wiki / editor / VTT and realtime / search / assets /
   plugin API / self-hosting and ops / accessibility / docs and website. Same no-auto-label
   note as the bug form.
5. `textarea` **alternatives** (optional) — including "do nothing".
6. `textarea` **constraints** (optional) — e.g. must not open an SSE stream on `/play`;
   must not put a second copy of game state in memory. The architecture plan's hard rules
   are the constraints most worth surfacing here.
7. `checkboxes` **design-impact** (optional) — "changes the data model", "changes the URL
   scheme or WebSocket protocol", "changes what a plugin may do", "changes `[!secret]`
   handling". Any ticked routes the issue to `status: needs-design` at triage.
8. `checkboxes` **willing** (optional) — "I would send a PR for this". Ticking is the signal
   for `good-first-issue`.

### 5.3 `03-question.yml`

`title: "[question] "`. `labels: ["type: question"]` — deliberately **without**
`status: needs-triage`. A question is a stub pointing at a Discussions thread; putting it in
the triage queue would be a category error.

1. `markdown` — questions are answered in Discussions; this issue is a signpost.
2. `textarea` **question** (required).
3. `textarea` **context** (required) — what you were trying to do, and which doc you read.
4. `textarea` **what-you-found** (optional).
5. `checkboxes` — "I read AGENTS.md and the linked design record", "I searched existing
   issues and Discussions".

### 5.4 `config.yml`

```yaml
blank_issues_enabled: false
contact_links:
  - name: My campaign's content is not rendering
    url: <discussions>/new?category=q-a
    about: >-
      Campaign content lives in your own Obsidian vault, so a rendering problem there is
      usually a vault or content problem rather than a semiplane bug. Ask here.
  - name: Security report
    url: <security advisories>/new
    about: Report privately. Do not open a public issue for a vulnerability.
```

The second contact link depends on private vulnerability reporting being enabled in repo
settings (§8). A contact link pointing at a disabled feature is a 404, and 404 on a security
link is the worst place to have one.

---

## 6. Pull request template

`.github/PULL_REQUEST_TEMPLATE.md`, one page, sections that this repo can actually answer:

- **What and why** — two sentences. The *why* is the part reviewers need and PR descriptions
  usually omit.
- **Closes #nnn**, or "not user-visible" — forces the no-issue case to be stated explicitly.
- **Gate** — a statement that `make ci` is green, and what was run if it was not. The
  template must not contain a checkbox the contributor cannot honestly tick; `make ci` runs
  locally, so "CI is green" is verifiable by the reviewer without trusting the description.
- **Design impact** — a checklist whose items are exactly this repo's irreversible surfaces:
  data model (new forward-only migration, no edits to a shipped one), URL scheme, WebSocket
  protocol or intent vocabulary, `[!secret]` handling, `System` interface / plugin authority,
  access tiers, cache keys. Each is a checkbox because each is expensive to undo.
- **Security** — one line: did this touch path confinement (`os.Root`), sanitisation
  (`html.WithUnsafe()`), authorization, or secret redaction? If yes, name the test that
  covers it. The architecture plan's secret-redaction test is the highest-value one in the
  repo and the template should name it.
- **Agent assistance** — this repo is agent-driven (`AGENTS.md`, three skills, `kilo.json`).
  State whether an agent produced the change and which instructions or skill it followed.
  An agent-authored change that skipped the quality-gate skill is the failure mode this
  catches.
- **For reviewers** — what you want looked at hardest, and what you deliberately left out.

---

## 7. CI and Pages workflows

### 7.1 `.github/workflows/ci.yml`

Triggers: `pull_request`, `push` to `main`, `workflow_dispatch`.
`permissions: contents: read, issues: read`.
Concurrency: group per ref, `cancel-in-progress: true` for PRs only — a superseded PR head
build has no value, but a superseded `main` build that already started deploying does.

| Job | What it does |
|---|---|
| `gate` | `actions/checkout@v7` → `actions/setup-go@v7` with `go-version-file: go.mod`, module cache on → install `golangci-lint` at the `Makefile`'s pinned version → `make ci` |
| `docs` | install Hugo at the `Makefile`'s pinned version → `make site-check` |
| `labels` | `GH_TOKEN: ${{ github.token }}` → `make labels-check` |

Details that will otherwise cost a debugging cycle:

- `go.mod` declares `go 1.27.1`, so `setup-go` resolves the toolchain from the file. Do not
  also pass `go-version`; `go-version` wins and would create a second source of truth.
- Do **not** set `cache-dependency-path: go.sum` — there is no `go.sum` until the first
  dependency lands, and a glob that matches nothing is a warning at best.
- `make ci` runs `go test -race`, which needs cgo. `ubuntu-latest` ships gcc; do not switch
  the runner to one that does not.
- Tool binaries go to `GOBIN=${{ runner.temp }}/bin` with that directory prepended to
  `PATH`. `$HOME/go/bin` is not reliably on `PATH` on a hosted runner.
- `labels` needs `issues: read`; `GITHUB_TOKEN` already has it, so no secret is required.

### 7.2 `.github/workflows/pages.yml`

Triggers: `push` to `main` and `workflow_dispatch`. **No `paths:` filter** — the site
includes `.kilo/plans/*.md` at build time, so a path filter covering only `docs/**` would
silently skip the deploy when a design record changes. A Hugo build over this site is
sub-second; correctness beats the saved seconds.

`permissions: contents: read, pages: write, id-token: write`.
`concurrency: {group: pages, cancel-in-progress: false}` — do not cancel in-flight
deployments.

Build job: `checkout@v7` → `setup-go@v7` → `GOBIN` Hugo install →
`actions/configure-pages@v5` for the base URL → `make site` with
`HUGO_ENVIRONMENT=production` → `actions/upload-pages-artifact@v3` with `path: docs/public`.
Deploy job: `actions/deploy-pages@v4`, `environment: github-pages`, `needs: build`.

> **Version note:** GitHub's own Hugo starter workflow currently pins
> `actions/deploy-pages@v5`, while the `deploy-pages` repository's own README still documents
> `@v4`. Pin `@v4` — it is the version the action's own documentation demonstrates — and let
> Dependabot's `github-actions` ecosystem propose the bump. Do not hand-pick `@v5` on the
> strength of a starter template that GitHub does not appear to keep current.

### 7.3 Makefile additions

```make
HUGO_VERSION   := v0.167.0
GOLANGCI_VERSION := v2.14.0        # move the existing pin out of `make tools` into a var
SITE_DIR       := docs
SITE_PUBLISH   := $(SITE_DIR)/public
```

| Target | Command shape |
|---|---|
| `tools` | additionally `CGO_ENABLED=0 go install github.com/gohugoio/hugo@$(HUGO_VERSION)` |
| `site` | `cd $(SITE_DIR) && hugo --minify --gc --baseURL $(BASEURL)` |
| `site-check` | same, plus `--panicOnWarning`, into a temp publish dir |
| `site-serve` | `hugo server -D` with `--bind 0.0.0.0` off (local only) |
| `labels` / `labels-check` | `scripts/sync-labels.sh apply` / `check` |
| `lint-workflows` | `actionlint` |

Three decisions embedded in that table:

- **`CGO_ENABLED=0` is load-bearing.** With cgo enabled, `go install` builds the *extended*
  edition, which drags in a C compiler for a site that uses no Sass. Pinning `CGO_ENABLED=0`
  makes the edition deterministic instead of dependent on the build machine.
- **`make check` does not depend on Hugo.** The Go gate must not fail because a docs
  toolchain is missing; the `docs` CI job is what gates the site. (Contrast the UI plan's
  §14.1, where `make check` *does* depend on `css` and `templ` — those produce runtime
  assets the binary serves, unlike Hugo.)
- **`actionlint` is a Go tool**, installed the same way as the rest: it fits
  `go install` and the existing `make tools` pattern, and it is the only way the workflow
  YAML gets any validation at all.

`make clean` additionally removes `$(SITE_PUBLISH)` and `$(SITE_DIR)/resources`. `docs/public`
and `docs/resources` go in `.gitignore`.

---

## 8. Repo settings (manual, not files)

These are prerequisites; the plan is not done until they are set, because three of the
committed files depend on them.

1. **Settings → Pages → Source: GitHub Actions.** Without this the deploy job fails.
2. **Settings → General → Discussions: enabled.** Both `config.yml` contact links 404
   otherwise.
3. **Settings → Security → Private vulnerability reporting: enabled.** The security contact
   link and `.github/SECURITY.md` both point here.
4. **Repository topics:** `self-hosted`, `ttrpg`, `vtt`, `wiki`, `golang`, `sqlite`,
   `obsidian`. Topics are the cheapest discovery surface that exists.
5. **Settings → Branches → branch protection on `main`:** require the `gate` check to pass
   before merge. This is what makes the PR template's "CI is green" claim true rather than
   aspirational.
6. **About → Website:** set to the Pages URL once the first deploy succeeds.

---

## 9. The Pages site

### 9.1 Build shape

`docs/hugo.toml`: `baseURL` from the deploy step, `defaultContentLanguage: "en"`,
`defaultContentLanguageInSubdir: false` (so English URLs carry no prefix and adding a
language later does not move every existing link), `enableGitInfo: false` (no
`Lastmod`-from-commit churn on a repo with two commits), `enableRobotsTXT: true`, and
`params.planDir = "../.kilo/plans"` so the include shortcode never hard-codes a relative
path.

### 9.2 Design records

`layouts/shortcodes/design-record.html`:

- `os.ReadFile` the plan named in the shortcode's argument, from `site.Params.planDir`.
- Fail the build with `errorf` if the file is missing or unreadable. A missing design record
  must be a red build, never a silently empty page.
- **Strip the leading `# ` H1 line** before `markdownify`; the page's front matter supplies
  the title. Leaving it in produces two `<h1>`s, which the UI plan's §7.2 makes a
  gate-blocking structural failure — the docs site holds itself to the same rule.
- `markdownify` uses Goldmark, the same engine the application renders campaign content
  with. What renders on the site is what renders in the app, by construction rather than by
  convention.
- Front matter carries `banner`: a callout stating this is a dated design record from
  2026-09-30, that it is **not** canonical user documentation, and that the docs pages are.

**Mermaid.** Both plans contain ```mermaid blocks, and Goldmark renders them as code
fences. Three options, in preference order: vendor a pinned `mermaid` ESM bundle under
`assets/js/`, fingerprint it with `resources.Fingerprint`, and load it only on `/design/`
pages — no CDN, no runtime integrity problem, cache-busted. If that task is deferred the
diagrams degrade to code blocks, which is acceptable for a record page. **Do not** add a CDN
`<script>`: a runtime third-party origin on the security documentation of a project whose
architecture plan has a threat model is not a trade worth making for diagrams.

Wide tables: the plans are table-heavy. Wrap tables in a scroll container in the site's CSS
and give them a `data-testid`. The UI plan's §7.9 reflow rule applies to the docs site too.

### 9.3 Token reuse

`docs/assets/css/tokens.css` holds a copy of the primitive/semantic/component token layers
and both theme blocks from the UI plan's §6, so the site ships light and dark with the same
contrast behaviour as the application. When `internal/web` gains its real
`tokens_contrast_test.go`, the same assertions should cover the docs copy — a second
assertion over a second file, not a second palette. Until `internal/web/tokens.css` exists,
this file is authored from the plan's tables and is the source the app later copies from.

### 9.4 Content

| Page | Source |
|---|---|
| `/` | Hand-written. What semiplane is, the three-sentence pitch, a screenshot placeholder, a link to the installation guide, badges. No marketing page — this is a self-hosted tool |
| `/install/` | Hand-written. Binary, `make run`, first `admin create`, first campaign. Must be runnable end to end by someone who has never seen the repo |
| `/concepts/` | Architecture plan §2.1 terminology and §4 data model, condensed. This is the page that stops people from saying "session" when they mean "campaign" |
| `/concepts/content-model` | Markdown, front matter, `kind`, wikilinks, embeds |
| `/concepts/secrets` | `[!secret]`, the reveal ledger, and §5.6.5's explicit statement that this hides from viewers and does not encrypt on disk |
| `/concepts/plugins` | The `System` interface, data packs, overlays, house rules, the kind-ownership table from §10.2.1 |
| `/operating/` | Backup (content root **and** a `.backup`/`VACUUM INTO` snapshot — never a raw copy of a live file), upgrade path, reverse proxy, the HTTP/2 recommendation that lifts the browser connection ceiling |
| `/operating/security` | The trust boundary, the §12 boundary table, and how to report a vulnerability |
| `/contributing/` | `AGENTS.md`, `make ci`, the PR process, the label axes |
| `/roadmap` | Architecture plan §15's twelve phases |
| `/design/` | The two plans, bannered |

### 9.5 Repository topics and social

`static/og.png` at 1200×630 for link previews, since the Pages URL will be pasted into
issue comments. `robots.txt` allowing everything — there is nothing to hide.

---

## 10. `.github/dependabot.yml`

`gomod` at `/` and `github-actions` at `/`, both weekly, both grouped so action bumps
arrive as one reviewable PR rather than five. `gomod` is a no-op while the module has zero
dependencies and future-proofs the first one. Add `docker` in the same commit as the
Dockerfile, not before.

---

## 11. `.github/SECURITY.md`

Short and specific rather than a template:

- **Supported versions** — a table whose only row is the latest release. There are no
  releases yet; say so, and say that unreleased `main` is not a supported target.
- **How to report** — GitHub private vulnerability reporting (Settings → Security), with an
  explicit "please do not open a public issue" and an expected response window that is
  honest rather than impressive.
- **What counts** — drawn from the architecture plan's §12 boundary table: secret leakage
  through any response path, path traversal outside `os.Root`, authorization bypass across
  campaign visibility, WebSocket origin, sanitisation, the secret sync-reconciliation cap.
- **What does not count** — §5.6.5 restated: anyone with filesystem access to the vault can
  read every secret, revealed or not. A report framed as "secrets are not encrypted at rest"
  is documentation, not a vulnerability, and saying so up front is kinder than a
  three-round email exchange.

---

## 12. Implementation order

1. **`.github/labels.yml` + `scripts/sync-labels.sh` + `make labels`.** First, because
   everything else references labels and forms silently drop absent ones. Run
   `make labels`, then `make labels-check`.
2. **The three forms + `config.yml`**, in `01-`/`02-`/`03-` order.
3. **`.github/PULL_REQUEST_TEMPLATE.md`.**
4. **`.github/dependabot.yml`.**
5. **Makefile**: extract `GOLANGCI_VERSION`, add `HUGO_VERSION`, `SITE_DIR`, the five site
   and label targets, `clean`, and `.gitignore` for `docs/public` + `docs/resources`.
6. **`.github/workflows/ci.yml`** — `gate` first and green on its own; then `labels`; then
   `docs` (which cannot pass until step 7 exists, so add that job last).
7. **`docs/`**: `hugo.toml`, layouts and partials, `assets/css/tokens.css`, then content
   pages in the §9.4 order, landing page last.
8. **`layouts/shortcodes/design-record.html`** + the two `/design/` pages.
9. **`.github/workflows/pages.yml`.**
10. **`.github/SECURITY.md`**, then the six settings in §8, in that order — the security
    report link is the one that must not be dead.
11. **Rewrite `README.md`** to point at the Pages site, the installation guide, and the
    design records. It is currently two lines and is what every visitor sees first.

## 13. Validation

Ordered cheapest first. Steps 1–3 are the ones that catch real mistakes.

| # | Check | Expected |
|---|---|---|
| 1 | `make ci` | Unchanged and green. No Go file is touched by this plan; if it goes red, something else changed |
| 2 | `make labels-check` | Passes. Re-run after editing one manifest colour to confirm it fails, then restore |
| 3 | `actionlint` via `make lint-workflows` | No findings on `ci.yml` and `pages.yml` |
| 4 | `make site-check` | Hugo builds with `--panicOnWarning` clean — no broken internal refs, no missing layouts, no missing shortcode targets |
| 5 | `make site-serve`, walk every page | Light and dark both legible; `/design/` pages carry the banner and exactly one `<h1>`; mermaid renders or degrades to code blocks; no page is a stub |
| 6 | Open one issue from each of the three forms | The right `type:` and `status:` auto-apply. **This is the only way to prove the label-bootstrap ordering worked**, and it is also the only way to see how the forms actually read — file them, read them as a contributor, then close them |
| 7 | Open a PR against a branch | The template renders; the `gate` check appears and must pass before merge (§8.5) |
| 8 | First Pages deploy | `https://popinjayjohn.github.io/semiplane/` serves; both contact links and the security link resolve; the `about` website field is set |
| 9 | Agent-assisted link sweep of the built site | Every internal link resolves. Per the UI plan's §10.9, a finding here becomes a committed check, not a memory |

## 14. Risks

| Risk | Impact | Mitigation |
|---|---|---|
| Forms committed before labels exist | Issues arrive with no `type:` and no `status:`, and nothing errors | Step ordering, plus the `labels` CI job |
| Repo settings not applied | Contact links and the security link 404; the deploy job fails | §8 is a checklist in the plan, not a footnote. A 404 on the security link is the worst case on the list |
| `go install` builds Hugo's extended edition on a cgo machine | A C compiler is required for a site with no Sass, and local and CI builds differ | `CGO_ENABLED=0` in the Makefile |
| `GOBIN` not on `PATH` in CI | Tool step fails with a bare "not found" | Explicit `GOBIN` + `PATH` prepend in both workflows |
| A design plan moves or is renamed | The `/design/` build fails, or worse, renders empty | The shortcode uses `errorf` on a missing file — a red build, never an empty page |
| Docs pages contradict shipped code | The site becomes actively misleading, which is worse than no site | Every behavioural claim links to its owning plan section or source file; `/design/` is bannered as dated |
| A `paths:` filter is added to the Pages trigger later | Design-record edits stop deploying | No filter by default, with the reason in a comment at the trigger |
| `status:` accumulates on closed issues | The triage query stops meaning anything | A note in `AGENTS.md` and the contributing page: a closing PR or a closing comment clears the `status:` label |
## 15. Out of scope

Release and tag-triggered build workflows. `CODEOWNERS`, `FUNDING.yml`,
`CODE_OF_CONDUCT`. A static sample campaign. Site search. Comments. Analytics.
Non-English content. Dependabot `docker` before a Dockerfile exists. A
`good-first-issue` / `help-wanted` label set — `good-first-issue` is in the flag
list, `help-wanted` is not, because it promises triage attention that a
single-maintainer repo cannot commit to.

---

## 16. Implementation notes

Five things in this plan did not survive contact with the tooling. Each is
recorded here with the reason, because the next person will otherwise try the
same thing.

### 16.1 Hugo cannot read `.kilo/plans/` — the records are staged, not mounted

§9.2 said `os.ReadFile` with `params.planDir`. That does not work. Hugo sandboxes
`os.ReadFile` to the project root: a `../` path resolves to nothing, and an
absolute path outside `docs/` does too. Critically, **the failure is an empty
string rather than an error** — precisely the silent-empty-page outcome the
shortcode exists to prevent.

A `[module]` mount of `../.kilo/plans` into `assets/plans` does work, and was the
first implementation. It has to be abandoned: **declaring `[module]` mounts makes
this Hugo build publish every page under `/en/` regardless of
`defaultContentLanguageInSubdir = false`**, producing the site twice, at `/` and
at `/en/`, with different canonical URLs.

Replaced by `make site-plans`, which copies `.kilo/plans/*.md` into
`docs/assets/plans/` (gitignored). Every site target depends on it, so the
deploy job and the local build cannot diverge. Read the records with
`resources.Get`.

Two related traps, both found the hard way:

- **`contentDir` must be declared per language.** With `content/en/` left to
  auto-detection, Hugo publishes under `/en/` and ignores
  `defaultContentLanguageInSubdir`. With `contentDir = "content/en"` declared
  explicitly, the flag is honoured.
- **In TOML, a top-level key placed after `[languages]` is silently read as
  `languages.<key>`.** An `enableGitInfo` that ended up nested took
  `defaultContentLanguageInSubdir` down with it. All top-level keys now precede
  every table, and `hugo.toml` says so at the point where it matters.

### 16.2 Hugo 0.167's table render hook cannot rebuild a table

§9.2 wanted each table in a scroll container with a `data-testid`. The render
hook's context in 0.167 is `tables.tableContext`, whose `tHead` and `tBody` are
**unexported**, and there is no `.Text` or `.Inner` — so the hook cannot emit the
table at all, wrapped or otherwise.

Replaced with CSS: below the md breakpoint, `table { display: block;
overflow-x: auto; min-width: 32rem }`. The table element becomes its own scroll
box, which keeps row and cell semantics for assistive technology while stopping a
wide table from widening the page. No `data-testid` — there are no committed
browser tests for the docs site, so a testid would assert nothing.

### 16.3 `static/robots.txt` is silently replaced

With `enableRobotsTXT = true`, Hugo generates `robots.txt` from an embedded
template and **overwrites** any `static/robots.txt`; the files are not merged. The
custom file had to become `layouts/robots.txt`.

### 16.4 `yq` is a new tool dependency

The label manifest cannot be read by `jq`, which is the obvious tool: `jq` parses
JSON, and the manifest is YAML with comments. `name: type: bug` unquoted is not
even valid YAML — a colon-space inside a plain scalar — so every string value is
now quoted.

`yq` is added to `make tools` and to the CI `labels` job for exactly one job:
converting the manifest to JSON. It is a pure-Go binary installed by `go install`,
which is the existing pattern for every other tool here. The alternative was an
awk state machine in `sync-labels.sh`, which is a worse thing to maintain than a
pinned tool.

A second bug lived in the same script: the remote-label key normalised spaces to
dashes so that GitHub's default `good first issue` would match the manifest's
`good-first-issue`, but the same transform mangled our own `area: wiki` into
`area:wiki`. The prune pass therefore deleted twelve of the project's own labels.
Keys are now a plain downcase, and deletion resolves back to the real name.

### 16.5 Two committed checks were added beyond the plan

§13 step 9 said an agent-assisted link sweep's findings should become a committed
check. Two did:

- `scripts/check-site-links.sh` — resolves every internal anchor against the
  built output. Wired into `make site-check` and the Pages build.
- `scripts/check-site-structure.sh` — asserts the §7.2 gate-blocking invariants:
  exactly one `<h1>`, a `lang` attribute, the skip link as the first anchor, no
  positive `tabindex`, and all four landmarks.

Both had to be written against **minified** output, because that is what Hugo
produces and the minifier strips attribute quotes. A check written against quoted
HTML passes a dev build and fails a production one, which is a check that reports
whatever the build flags happen to say. The link checker also asserts that it
found links at all, because a checker that silently matches nothing is worse than
no checker.

### 16.6 Repo settings: what was applied and what was not

**Applied.** All 27 labels exist on the remote and `make labels-check` passes.
The five GitHub default labels (`bug`, `enhancement`, `documentation`,
`accessibility`, `question`, `help wanted`, `invalid`, `wontfix`, `good first
issue`) were pruned, because a double vocabulary in the sidebar is worse than no
default set.

**Still manual.** Everything in §8 except the labels:

- Pages source → GitHub Actions
- Discussions enabled — both `config.yml` contact links 404 without it
- Private vulnerability reporting — the security contact link 404s without it
- Repository topics — `gh repo edit --add-topic` returns
  `HTTP 403: Resource not accessible by personal access token`, so the token
  lacks the scope. Needs an admin-scoped token or the web UI.
- Branch protection requiring `gate` on `main`
- The About website field, after the first deploy

Validation steps 6, 7 and 8 (§13) also remain: filing one issue from each form to
prove the labels auto-apply, opening a PR to prove the template renders, and
watching the first deploy. None can be done from this worktree.
