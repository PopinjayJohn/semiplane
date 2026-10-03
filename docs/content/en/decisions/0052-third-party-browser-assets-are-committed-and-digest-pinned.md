---
title: "0052 — Third-party browser assets are committed, digest-pinned, and served from our own origin"
description: "PixiJS and Datastar are committed under `internal/web/static/vendor/` with a sha256 per file and the upstream archive integrity, `make vendor` re-fetches and verifies, and `make vendor-check` re-hashes the committed bytes with no network at all."
lede: "A campaign-scoped, reader-authorised assets route means a CDN is a second origin this product would have to authenticate a player to, and a build that reaches the network is a build whose output depends on a third party being up. The cost is a committed blob and a `vendor-check` that has to actually run — which is the whole of this record, because a pin nobody verifies is a comment."
weight: 3
date: "2026-10-03"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

Phase 9 adds two third-party pieces of JavaScript to the browser: the renderer
for the live tabletop, which
[0015]({{ "decisions/0015-pixijs-and-client-surface/" | relURL }})
chose as PixiJS v8, and the patcher for the live chrome, which
[0014]({{ "decisions/0014-datastar-scope/" | relURL }}) scoped to Datastar over
SSE. Both are libraries this repository did not write, both are served to
readers, and both are **executable code running in a reader's browser with the
authority of the page they are on**.

Two properties of this product make the obvious answers wrong, and they pull in
opposite directions from how a front-end project normally resolves a dependency.

**First, the assets route is campaign-scoped and reader-authorised.** S-8 answers
`/c/{slug}/assets/**` through a gate, and
[0024]({{ "decisions/0024-authorisation-gates-mount-not-handler/" | relURL }})
is the rule that the gate is mounted rather than checked. A CDN is therefore a
**second origin this product would have to authenticate a player to**: the
access matrix would have to be reproduced somewhere that is not this codebase,
for an asset that is the same bytes for every reader of every campaign. It is
also an origin the campaign's gate never touched, which means the one class of
byte this codebase can actually vouch for — bytes it holds and hashes — is the
one class a CDN cannot supply.

**Second, this repository ships one static binary.**
[0001]({{ "decisions/0001-go-as-implementation-language/" | relURL }}) is the
decision to have no runtime to install, and a self-hosted instance is frequently
on a network with no route to a registry. A build that fetches its own front-end
dependencies is a build whose output depends on a third party being up, and a
product whose stated property is "air-gap friendly" cannot have its own assets
behind somebody else's uptime.

[0015]({{ "decisions/0015-pixijs-and-client-surface/" | relURL }}) already
committed to the answer in a single line — "a committed, integrity-pinned file
served from the app's own origin", pointing at D13 in the delivery plan — and
phase 9's plan resolved it the same way. Neither shipped a record, and the
phase's Definition of Done requires one. This is that record, and it covers
**both** assets rather than only the one that happened to land first: the second
package is added to the same `packages` array by the same mechanism, and nothing
in the Makefile changed to accommodate it.

## Decision

**Every third-party browser asset is committed under
`internal/web/static/vendor/`, declared in `tools/vendor.json`, and held to two
digests that are not interchangeable.**

- `source.integrity` is the upstream's own Subresource Integrity value: the
  **sha512 of the source archive**. The archive is never committed, so this is
  the digest only `make vendor` can check, against the bytes the source actually
  served.
- `files[].sha256` and `files[].bytes` describe the **committed file** — what
  `make vendor-check` recomputes from disk on every `make check`.

Comparing the archive digest in the offline check would be checking a file the
gate cannot see, and comparing the committed digest in the fetch would trust
whatever the source served. Both are present because the two questions are
different: *did the source give me the artefact it says it did?* and *are the
bytes on this disk the bytes this repository reviewed?*

### Two upstreams, and why the manifest names them rather than assuming one

`source.kind` is `npm` for a registry tarball and `archive` for a project's own
release tarball, and `tools/vendor` takes **one path for both**: an `https` URL,
an integrity digest checked *before* anything is decompressed, and one named
member walked out of the archive. `files[].origin` is `archive` for a file
extracted from that pinned source and `local` for a file this repository wrote —
the MIT licence is the only one, because MIT requires the notice to travel with
the code.

The distinction is bookkeeping, not behaviour, and the honest reason it exists
is that **Datastar is not served by a registry.** npm's
`@starfederation/datastar` stops at `1.0.0-beta.11`, and every build from
0.20.0 onwards dispatches on `datastar-merge-fragments`; the rename to
`datastar-patch-elements` this product emits happened at 1.0.0. So the pinned
source is a GitHub tag tarball for `v1.0.4`, and labelling it `npm` was a stated
falsehood in the one file whose entire job is being trustworthy about
provenance. A manifest whose `kind` is wrong for half its entries trains the
reader to stop reading it, which is the whole cost this decision is paying to
avoid.

The alternative was to leave the lie and document it in the manifest's
`description`, which is what first happened: a paragraph explaining that one entry
says `npm` and means something else. An apology in a data file is not a
correction, and the field was small enough to fix.

### Three targets, because there are three claims

| Target | Network | Claim |
|---|---|---|
| `make vendor` | yes | Re-fetch every pinned package, verify it, write the pinned bytes into the vendor tree. |
| `make vendor-dry-run` | yes | The same fetch and the same verification, writing nothing. |
| `make vendor-check` | **no** | Re-hash the committed bytes, and require every file in the served tree to be declared. |

`vendor-check` is in `check`, immediately after the format diff check and before
`make css`. It sits there for the reason `css` and `templ` sit before `build`:
the staged vendor tree is copied into `internal/web/static/dist/`, `internal/web`
embeds `all:static/dist`, and `build` therefore embeds **whatever vendored bytes
are on disk** into the shipped binary. A build that embedded a swapped blob
would produce a working binary serving somebody else's JavaScript while every
gate below that line reported green about it. It also reads a committed file
rather than a build output, which is the same category of check as the format
diff above it, and the cheapest place to find a problem.

**`vendor-check` reaches no network**, and that is a requirement rather than an
optimisation: CI has no npm account and no credentials, so a gate that fetched
there would make `make check` depend on a third party being up. It is also
**guarded**, in the same idiom as `a11y`, because `go test -run` exits **0**
when its pattern matches nothing — a pin checked by a gate that selected no
assertion is a pin that is trusted rather than checked. AGENTS.md records two
separate occasions on which this repository shipped a gate that was green and
looked nowhere.

`vendor-dry-run` exists because `vendor-check` structurally cannot answer the
question a maintainer asks when adding a package. Editing the manifest changes
the digests, so the offline check passes the instant the wrong digests are
written down: it compares the disk against the manifest, and both were just
edited. Confirming a manifest edit **against the registry**, before it is
committed, is a network operation and gets its own target.

### The check is a Go test, and it is not reimplemented

`vendor-check` runs `TestTheVendoredBytesMatchThePin`. It reads the whole
manifest rather than one package, so adding a package to `packages` is covered
without touching the Makefile, and it holds a second claim the Makefile could
not: **every file in the served vendor tree is declared**. A digest gate over
the manifest's own list cannot catch the interesting accident — a hand-copied
script dropped into a directory the product serves, which no digest covers
because nobody wrote one.

A second implementation in the Makefile was rejected for the reason this
repository keeps recording: two answers to one question is one more than a
repository can check, and the two would be free to disagree about what the pin
means. The fetching half has no such existing implementation and is
`tools/vendor`, a Go program — because a bounded HTTP read, a base64 Subresource
Integrity comparison, a gzip tar member lookup and a write that is refused
unless the digest matches are four things to get right and not one shell
pipeline to get right casually. Its tests use an `httptest` TLS registry, so the
fetcher is itself covered by `make check`, offline.

### `origin: "local"`, and why a licence is a pinned file

A file this repository wrote is committed and hashed like any other but has
nothing upstream to re-fetch, so `make vendor` verifies it in place and reaches
no network for a package whose files are all local. The one such entry today is
a licence notice, and it is a **declared file** rather than an afterthought
because MIT requires the notice to travel with the code — a licence that is
regenerable is a licence nobody can prove was ever there.

### The committed bytes have to be staged to be served at all

`internal/web` embeds `all:static/dist` and `/assets/` serves exactly that tree,
so anything the browser fetches must be **in** `static/dist/` or it 404s — and a
404 asset is a *silently* absent behaviour: the page renders, the tabletop loads,
and the map is a blank box. `make stage-assets` copies `static/js/**.js` and
`static/vendor/**` in with their relative layout preserved, because
`scene.js` imports `../../vendor/pixi.min.mjs` and a flattened copy is a module
graph that cannot resolve. `*.js` only, and not as an optimisation: those trees
also hold the `*_test.go` files that audit the modules beside them, and a copy
that took everything would embed a Go test file into the binary and serve it
from a route with no gate on it.

## Consequences

**The binary carries the blobs.** PixiJS `browserAll` is 841KB committed and
841KB embedded, and Datastar is small beside it. That is the price, and it is
paid in exchange for a build that needs no registry and an instance that can
serve the map with no egress at all. `pixi.min.mjs` ends in a
`sourceMappingURL` comment naming a file this repository deliberately does not
ship: a devtools 404, on purpose, because a 2.2MB map for an 841KB module is
not a cost a self-hosted instance should carry.

**A version bump is a diff in `tools/vendor.json` and one command.**
`make vendor-dry-run` confirms it against the registry, `make vendor` writes it,
`make vendor-check` confirms the disk. Upstream re-publishing a version is a real
event — npm permits it — and the pin turns it into a loud failure that names both
digests rather than a silent change of what the product serves.

**The pin is a provenance claim, not a safety claim.** It says these bytes are
the ones a human read a diff for. It does not say they are free of anything, and
a digest gate that is mistaken for an audit is worse than none, because it reads
as an assurance nobody made. What is checked beyond the digest is deliberately
narrow: `TestTheVendoredFileIsThePinnedOneBytes` reads the version string the
build itself embeds, because a digest regenerated from the *wrong* tarball is
consistent with itself.

**`tools/vendor` is inside the module, so the ordinary gate covers it.** `go
vet`, `golangci-lint` and `go test -race ./...` reach `tools/`, which means a
fetcher nobody tested could not be committed. That is the argument for a Go
program over a shell script: the shell alternative would need its own harness, and
the repository has one toolchain.

**A missing target is a silent absence, and this phase produced two reports of
one.** Two work items independently discovered that `/assets/` serves
`static/dist/`, that nothing staged `static/js/` or `static/vendor/` into it, and
that neither could fix it because the Makefile belongs to the integrator. The
symptom in a browser is a script tag that resolves to nothing: no error in the
build, no error in the gate, and a feature that is simply not there.

## Alternatives considered

**A CDN, or a public npm URL in a `<script src>`.** Rejected on both halves of
the argument above. It is a second origin the access matrix would have to be
reproduced in, for bytes identical to every reader; it makes the product
dependent on a third party's uptime and on a self-hosted instance having egress;
and — the part the pin makes concrete — a URL that resolves to different bytes
at two points in a campaign's life is a property no digest can catch, because
there is no digest to compare against. It also moves the trust boundary to a
party this codebase cannot audit at all.

**Fetching at boot.** Rejected: a network call on every start, or a cached copy
of unrecorded age, in front of a route whose answer must be reproducible. An
instance with no egress starts broken, and "broken at boot" is a worse failure
than a larger binary.

**A bundler that pulls from npm at build time.** Rejected because it adds a Node
toolchain to a repository whose premise is one static binary, because it makes
the build depend on a registry being up, and — the load-bearing part — because a
bundle's bytes exist in no commit. A supply-chain change would then be invisible:
the lockfile moves, and the artefact that changed is not in the tree and not in
any diff. Pinning the *inputs* and committing the *outputs* is what makes the
review possible.

**Shipping no vendored assets at all.** Rejected on the measured argument in
[0015]({{ "decisions/0015-pixijs-and-client-surface/" | relURL }}): a map
holding a few hundred sprites with fog is a renderer, and reimplementing one is
not this project's problem. The same applies to
[0014]({{ "decisions/0014-datastar-scope/" | relURL }})'s patcher. "No
dependencies" is not free either — it is a dependency on code nobody has
audited, written by whoever was nearest.

**Pinning by version string, with no digest.** Rejected: a version is a claim and
a digest is the check. Every failure this pin exists for — a re-published tag, a
compromised mirror, a hand-edited file — is invisible to a version string.

**Committing the tarball rather than the members it contains.** Rejected: it
removes the extraction code and it ships 2.2MB of unminified twin and source
maps that no page loads, for the same bytes.
