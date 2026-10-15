# Semiplane — Technology Stack

*Status: Approved direction · Last updated: 2026-10-15*

Semiplane is a cross-platform, single-binary web application: a TTRPG wiki and
campaign manager whose source of truth is a folder of plain Markdown files. It
supports realtime GM→player reveals, player-owned character pages, rules-as-Markdown
dice hooks, and a guided character creator.

This document is the canonical record of the stack **and the reasoning behind each
choice**, so contributors (and future us) don't relitigate settled decisions.

---

## 1. Design Principles

1. **One binary, zero runtime dependencies.** `go build` → one file that runs on
   Windows, macOS, and Linux. No installer, no Docker, no database server, no CGo.
2. **The filesystem is the source of truth.** All content is plain `.md` with YAML
   frontmatter. Anything else (SQLite index, caches) is derived and rebuildable.
3. **No vendor lock-in.** Quitting Semiplane leaves you with valid Markdown that
   Obsidian, VS Code, or Git can read forever.
4. **API-first.** The web UI is one client of a documented REST API; mobile apps are
   equal citizens.
5. **System-agnostic.** No game rules are hardcoded into the binary. Rulesets,
   classes, dice systems, and character sheets live in Markdown.
6. **Cheap on ops.** SSE over WebSockets, embedded SQLite over Postgres, stdlib over
   frameworks — every choice minimizes what a GM must run and what we must maintain.

---

## 2. Architecture Overview

```
┌────────────────────────────────────────────────────────────────┐
│                        Single Binary                           │
│                                                                │
│  ┌──────────────────┐        ┌──────────────────────────────┐  │
│  │  Embedded SPA    │  REST  │      Go Backend (stdlib)     │  │
│  │  Svelte 5 + Vite │◄──────►│  /api/v1 (http.ServeMux)     │  │
│  │  TipTap editor   │  SSE   │  Auth · Permissions · Hooks  │  │
│  └──────────────────┘◄───────│  Goldmark AST · Dice engine  │  │
│                              │  Query & Subscription hub    │  │
│                              └──────────────┬───────────────┘  │
│  ┌──────────────────┐                      │                  │
│  │ Compiled-in      │  ┌───────────────────▼────────────────┐  │
│  │ Go Plugins       │─►│ SQLite (modernc) index             │  │
│  │ (init registry)  │  │ FTS5 · backlinks · sessions · ACLs │  │
│  └──────────────────┘  └───────────────────┬────────────────┘  │
│                                            │ fsnotify          │
│                              ┌─────────────▼──────────────┐    │
│                              │  User Vault (Markdown)     │    │
│                              │  campaign/ rules/          │    │
│                              │  characters/ assets/       │    │
│                              │  .semiplane/plugins/       │    │
│                              └────────────────────────────┘    │
└────────────────────────────────────────────────────────────────┘
        ▲ TLS termination (optional) ── Caddy / nginx / Cloudflare Tunnel
```

---

## 3. Core Stack (with rationale)

### 3.1 Backend

| Component | Choice | Why |
|---|---|---|
| Language | **Go** | Static single-binary compilation with `CGO_ENABLED=0`; ~20 MB RAM; cross-compiles to all targets from one machine. The ecosystem's proof cases (Caddy, Hugo, PocketBase, Gitea) are exactly this use case. |
| HTTP server | **Go stdlib `net/http` + `http.ServeMux`** | Go 1.22+ `ServeMux` has native method routing and path wildcards (`GET /api/v1/notes/{path...}`). A framework buys nothing we need and costs API churn, dependency risk, and abstraction drift. |
| Realtime push | **SSE** (stdlib `http.Flusher`) | Cheaper than WebSockets for a broadcast model: built-in browser reconnection with `Last-Event-ID` replay, survives mobile sleep/Wi-Fi flapping, traverses corp proxies, zero dependencies. See §7. |

**Explicitly rejected:** Chi/Huma/Echo (unnecessary router abstraction), Rust+Axum
(great AST power, but goldmark matches this workload and Go's pointer-based tree
mutation is more ergonomic than fighting the borrow checker on cyclic AST graphs).

### 3.2 Content Pipeline

| Component | Choice | Why |
|---|---|---|
| Markdown engine | **Goldmark** (`github.com/yuin/goldmark`) | CommonMark-compliant with a three-phase pipeline: Parsers → AST Transformers → Renderers. Hugo-proven. Custom syntax (wikilinks, statblocks, `:::gm` blocks) plugs in cleanly at each phase. |
| Dice grammar | **Participle** (`alecthomas/participle`) | Declarative struct-tag grammar that produces typed ASTs in Go — robust for `4d6kh3 + @mod`-style expressions, and makes symbolic-dice systems representable. |
| Rule hooks | **Markdown in `rules/` + `expr-lang/expr`** | Hooks stay user-editable plain text while conditions/formulas are evaluated by a sandboxed, pure-Go expression engine. No recompilation to change the rules. In detail: hooks are typed records over a closed event/state ontology, never prose parsed at roll time — see §3.7. |
| Wikilinks/frontmatter | Goldmark metadata + `gopkg.in/yaml.v3` | Obsidian-compatible `[[links]]` and YAML frontmatter; arbitrary per-system character schemas need no migrations because SQLite never stores content. |

**The GM-secret pipeline (core domain feature):**
`:::gm` blocks become first-class AST nodes. A role-aware AST transformer strips
them server-side for players (never trust the client), and reveals are broadcast as
SSE `block_revealed` events containing pre-rendered HTML.

### 3.3 Storage & Search

| Component | Choice | Why |
|---|---|---|
| Database | **SQLite via `modernc.org/sqlite`** | Pure Go — no CGo, so cross-compilation stays trivial. Embedded, zero-config, transactional. |
| Full-text search | **SQLite FTS5** | BM25-ranked search over the corpus; no external engine to run. |
| Link graph / backlinks | SQLite relational tables | Backlinks and unlinked mentions are queried from an index built by AST traversal, not by scanning files per request. |
| File watching | **`fsnotify`** | External edits (Obsidian, VS Code) push live updates to connected clients. |

**Invariant:** SQLite is a *derived index*. `semiplane reindex` rebuilds it from the
Markdown at any time; losing `index.db` loses nothing.

### 3.4 Frontend

| Component | Choice | Why |
|---|---|---|
| Framework | **Svelte 5** (React + Vite as documented alternative) | Svelte compiles to tiny surgical JS (~150–300 KB gzipped), keeping the embedded binary lean and table devices fast; signals make realtime HP/initiative updates cheap. React remains viable if graph-view ecosystem (React Flow) or plugin-author familiarity outweighs bundle size. |
| Build | **Vite** + `//go:embed dist/*` | The SPA compile output is embedded directly into the Go binary — no external asset directory to ship. |
| Editor | **TipTap (ProseMirror)** | Hybrid editing: Markdown shortcuts and clean `.md` serialization to disk, plus **custom node views** — in-document dice buttons, statblock cards, clocks, and `:::gm` blocks. Live Preview keeps raw Markdown visible on the cursor line (Obsidian-familiar). |
| Styling | **Tailwind CSS** | Responsive GM desktop ↔ player phone/tablet from one codebase. |
| Character creator | SPA wizard driven by `rules/` metadata | Same "rules are data" philosophy as dice: ranger classes, ancestries, and point-buy rules come from Markdown, so new systems need no frontend code. |

### 3.5 Realtime

| Direction | Mechanism | Why |
|---|---|---|
| Client → Server (writes) | REST `POST/PUT/DELETE` | Predictable, cacheable, testable; the API stays the product surface. |
| Server → Client (live) | **SSE** `/api/v1/events` | Event volume is low-frequency broadcasts (file changed, block revealed, dice rolled, turn advanced). SSE gives native reconnect + `Last-Event-ID` replay for phones that slept through a roll. |
| Future VTT | Reserved `/ws/vtt` WebSocket endpoint | 60fps token dragging needs full duplex; adding one endpoint later doesn't disturb the SSE design. |

### 3.6 Authentication & Permissions

| Concern | Choice | Why |
|---|---|---|
| Password hashing | **Argon2id** (`golang.org/x/crypto/argon2`) | OWASP-recommended, memory-hard. Chosen over bcrypt for new applications. |
| Web sessions | **Opaque random tokens** in SQLite → `HttpOnly`, `SameSite=Lax` cookie | Revocable by DB row deletion; a stolen DB dump can't forge sessions (only hashes are stored). |
| LAN join | **6-digit codes / QR scan** | Zero-friction at the table; one-time tokens, no passwords to type on phones. |
| Remote identity | **OAuth2/OIDC** (`golang.org/x/oauth2` + `coreos/go-oidc`, Authorization Code + **PKCE**) | Correct flow for mobile: system-browser tabs, no embedded webviews, no client secrets on devices. |
| API tokens | Opaque, scoped, revocable Bearer tokens | Better than JWTs here: instant revocation, no key rotation. Scopes (e.g. `characters:write`) enforce least privilege. |
| Authorization | `gm` / `player` roles + path-based ACL middleware | `characters/<user>/*` ownership maps directly to the vault layout — no separate permission store to keep in sync. |
| Transport | **No in-app TLS** — reverse proxy (Caddy/nginx/Cloudflare Tunnel) | TLS is an ops concern, not app logic. The app honors `X-Forwarded-Proto` and sets the cookie `Secure` flag only when it knows it's behind TLS. OAuth/OIDC and PWA installability are documented as requiring the proxy. |

### 3.7 Operations

| Concern | Choice | Why |
|---|---|---|
| Logging | **`log/slog`** | Stdlib, structured, zero-dep. JSON handler in production, text handler locally; request-scoped via middleware + request IDs. |
| Metrics | `prometheus/client_golang` | Default-off, exposed on a separate port (`:9090/metrics`): HTTP latency, SSE connection counts, index rebuild duration, dice rolls by system. |
| Self-update | `minio/selfupdate` **with signature verification** | One-click binary updates via the web UI. Releases are signed (minisign/cosign) and verified before applying — an unsigned self-updater is an RCE vector. |
| Backups | `semiplane backup` — vault zip + `VACUUM INTO` | Correct online SQLite backup; the `.md` tree is the real backup target (Git-friendly by design). |
| Debugging | `semiplane doctor` | Log/config/path diagnostics bundle attached to bug reports. |

### 3.7 Rules Engine & Dice Hooks (normative semantics)

Decisions locked during the rules-engine spike. The full schema — canonical event
set, state model, hook record format — lives in `docs/rules-ontology.md`.

| Concern | Decision |
|---|---|
| Hook model | Hooks are **typed data records** over a closed, semver'd **event/state ontology**. A trigger is a structural match on a canonical event, never prose parsed at roll time. Automation happens at **ingest time only** (structural extraction + reviewed translation + type-check + simulation), never at runtime. |
| `expr` role | Conditions/formulas evaluate against **typed context** (attacker/target state, `used_ability`, resources, turn state). Sandbox and purity unchanged. |
| Advantage | An **event-level dice spec**, not a hook: 2d20kh1/kl1; advantage and disadvantage cancel to a straight roll; they never stack. |
| Damage slots | Every modifier declares a **slot**: dice or flat. On a critical hit, **only dice multiply**. Canonical precedence: inherent → ability → item → class → feat → misc. |
| Evaluator shape | Hooks are pure reducers `(state, event) → (state, effects)` with seeded RNG and no wall-clock: replayable, testable, safe under concurrent players. |
| One engine | The same server-authoritative engine resolves `/api/v1/dice/roll` today and the reserved `/ws/vtt` later; hook-mediated events broadcast via SSE; `.semiplane/oplog/` doubles as the replayable combat log. |
| Ontology ceiling | Homebrew outside the ontology maps to the plugin tier (§4), not to runtime NLP. Unknown vocabulary is refused at authoring time, never guessed at roll time. |

---

## 4. Plugin Architecture (Hybrid)

| Tier | Mechanism | Why |
|---|---|---|
| **Compiled-in** | Go packages registered via blank imports (`_ "semiplane/plugins/dnd5e"`) + `init()` registry; optional build tags (`-tags plugin_dnd5e`) | Preserves the single-binary promise. Community members fork one folder to build a bespoke binary. Interface surface lives in a semver'd `pkg/pluginapi` package with capability negotiation. |
| **Runtime (user)** | `.semiplane/plugins/` — JS/CSS dynamically imported by the SPA; hot-reloaded; installed via web UI | Marketplace UX (Obsidian/Foundry-familiar) without recompiling the binary for a theme or statblock renderer. |
| **Backend sandbox** | Optional WASM plugins via **`wazero`** | Pure-Go WebAssembly runtime — keeps zero-CGo compilation while allowing third-party logic (generators, dice variants) to run safely. |

---

## 5. Data Integrity & Safety (non-negotiable)

| Rule | Implementation |
|---|---|
| **Atomic file writes** | Write temp → fsync → rename. Crashes never truncate user prose. |
| **Hash-based change detection** | Content hashes on every write suppress fsnotify echo loops and detect concurrent external edits. |
| **External-edit conflicts** | Before save, compare on-disk hash to the editor's loaded hash; on mismatch present overwrite/reload/save-copy. Never silent last-write-wins on user content. |
| **Generated machine blocks** | Semiplane's ingest tools write machine data only inside the `semiplane:` frontmatter namespace or `<!-- semiplane:begin --> … <!-- semiplane:end -->` markers. Regeneration is marker-scoped and idempotent; user prose is never rewritten. Generator writes obey the same atomic-write and hash-conflict rules as any save. |
| **Path traversal guard** | Every filesystem route canonicalizes the path and asserts it remains inside the vault root. |
| **CSP headers** | Strict `Content-Security-Policy` — rendered Markdown and user plugins are attack surface. |
| **WAL mode + `busy_timeout`** | SQLite correctness under concurrent REST + SSE load; `VACUUM INTO` for backups. |
| **Index rebuildability** | `semiplane reindex` reconstructs everything from `.md` files. |
| **Vault migration** | `vault_version` in config + up-only, idempotent, logged migrations; refuse to open vaults from newer app versions. |

---

## 6. API Contract

- Base: `/api/v1` (frozen once mobile apps ship; breaking changes go to `/v2` with a
  deprecation window).
- Errors: RFC 7807 `application/problem+json` — one error-handling path for all clients.
- A version-controlled OpenAPI spec is validated in CI against the live routes.
- Pagination, rate-limit headers, and `ETag`/`If-None-Match` on note reads from day one.

Key endpoints (abridged):

```
GET    /api/v1/notes               # list/filter by tag, folder, type
GET    /api/v1/notes/{path...}     # content, metadata, backlinks
POST   /api/v1/notes/{path...}     # atomic upsert
GET    /api/v1/search?q=           # FTS5
GET    /api/v1/graph               # nodes/edges for link visualisation
POST   /api/v1/dice/roll           # { "formula": "2d20kh1+5" } → symbols/total
POST   /api/v1/reveal              # { "page", "blockId" } → SSE broadcast
GET    /api/v1/events              # SSE stream
GET    /ws/vtt                     # reserved for future realtime VTT
```

---

## 7. Why SSE, Not WebSockets (recorded decision)

The workload is low-frequency, server→client broadcasts. SSE gives us: built-in
reconnect with event replay (a player whose phone slept through a crit still sees
the roll), traversal of restrictive networks, native `EventSource` on every client,
and ~15 lines of stdlib Go with zero dependencies. WebSockets are reserved for a
future high-duplex VTT. WS was not rejected — it was scoped.

---

## 8. Rejected Alternatives (ADR summary)

| Option | Verdict | Reason |
|---|---|---|
| Rust + Axum + Tantivy | Rejected | Goldmark covers the AST requirements; Go's pointer-based tree mutation beats borrow-checker friction; Go's cross-compilation and build times are materially simpler. |
| WebSockets as primary transport | Rejected | Unnecessary duplex overhead; worse proxy/mobile behavior for a broadcast workload. SSE fits; WS reserved for VTT. |
| In-app TLS (self-signed/ACME) | Dropped | Reverse proxy is the industry answer; in-app TLS adds cert rotation and trust-UX burden the app doesn't need to own. |
| CHI/Huma/Echo frameworks | Rejected | Go 1.22 `ServeMux` covers routing; frameworks add dependency and abstraction cost with no feature we require. |
| JWT sessions | Rejected | Opaque revocable tokens fit a single-binary, single-DB deployment and kill key-rotation complexity. |
| bcrypt | Rejected | Argon2id is the current standard for new apps. |
| WASM-only plugins | Rejected | Browser-native JS/CSS runtime plugins are simpler for the 90% case (UI, themes, renderers); WASM is the escape hatch. |
| Dynamic libraries (.so/.dll) | Rejected | Destroys the single-binary promise; compiled-in + registry pattern (Caddy-style) instead. |
| PostgreSQL/MySQL | Rejected | An embedded, pure-Go SQLite index plus Markdown on disk removes the worst part of self-hosting: external services. |
| Rules hardcoded in Go | Rejected | Defeats system-agnosticism; `rules/` Markdown + `expr` keeps homebrew in the user's hands. |

---

## 9. Maintainability Commitments

- **Testing:** golden-file tests for the Markdown→HTML pipeline (its highest-risk
  code), property/fuzz tests for dice and `expr` hooks, `httptest` API integration
  against a fixture vault, cross-compilation on every main-bound PR and release tag.
  Extracted hooks must pass a seeded-RNG simulation harness (scenario + property tests)
  cross-validated against typed source JSON; a coverage report gates auto-promotion,
  everything else queues for human review.
- **Interfaces at boundaries:** `NoteRepository`, `SearchIndex`, `AuthProvider`,
  `DiceEvaluator` — so SQLite/FTS can be swapped for Tantivy, or local auth for
  LDAP, without touching handlers.
- **Plugin API semver:** `pkg/pluginapi` + capability negotiation + compatibility
  matrix, so plugin authors aren't broken by internal refactors.
- **CLI surface:** `serve`, `reindex`, `migrate`, `backup`, `user`, `plugin`,
  `doctor` — operational self-service for GMs and for us.
- **Config discipline:** flags > env > file > defaults, validated at boot, secrets
  only via env.
- **i18n & theming seams:** UI strings out of components; CSS custom properties for
  community skins — cheap now, expensive after launch.
- **Expansion seams:** reserved `/ws/vtt` route, to be served later by the same
  rule engine as `/api/v1/dice/roll` (§3.7); an append-only operation log
  (`.semiplane/oplog/`) as the seed for future multi-device sync and the replayable
  combat log; opt-in-only error reporting behind an interface (TTRPG content is
  private by default).

---

## 10. Vault Layout

```
my-campaign/
├── campaign/                 # GM-only notes, session logs, hidden lore
├── rules/                    # Rule hooks, classes, ancestries → drives dice + wizard
│   ├── classes/barbarian.md
│   └── hooks/sneak-attack.md
├── characters/
│   ├── throg/character.md    # player-owned (ACL: characters/<user>/*)
│   └── elora/character.md
├── assets/                   # maps and portraits, served directly
└── .semiplane/
    ├── plugins/              # runtime JS/CSS/WASM plugins
    ├── oplog/                # reserved: mutation journal for future sync
    ├── logs/
    └── index.db              # derived SQLite index — safe to delete & reindex
```

**Content corpus (spike baseline):** the seed corpus is 5e.tools-derived — a
2024 D&D extract (`xphb` / `xdmg` / `xmm` source tags) in Obsidian-flavored
Markdown plus typed source JSON (`actions.json` et al.). The typed JSON is the
preferred automation input; the Markdown is the user-facing surface, augmented in
place via the `semiplane:` namespace and marker blocks (§5), never regenerated
wholesale. Mixed SRD/non-SRD provenance per source tag gates what may be
redistributed.

---

**Bottom line:** Go stdlib + filesystem Markdown + SQLite + SSE + a Svelte 5 SPA.
Every component choice above optimizes for one property: *a GM can download a
single file, point it at their Markdown folder, and run a live campaign that night —
and we can still maintain it at version 2.*
