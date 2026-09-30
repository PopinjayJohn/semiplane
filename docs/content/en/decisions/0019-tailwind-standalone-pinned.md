---
title: "0019 — Tailwind as a pinned standalone binary, with the manifest hash committed"
description: "The CSS toolchain is a downloaded executable verified against a committed digest of its own release manifest, and `make check` builds CSS and templ before compiling Go."
weight: 190
date: "2026-09-30"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

Phase 1 is the point where the frontend toolchain has to exist, because every
later phase inherits the shape of the gate. Getting it wrong here means a
toolchain failure is diagnosed through eleven unrelated files, six phases later.

The toolchain is two things: the **templ** compiler, and a **Tailwind** build.
templ installs from the Go module proxy like every other Go tool here, so it is
unremarkable. Tailwind is not — the upstream recommendation is Node, and
[0001]({{ "decisions/0001-go-as-implementation-language/" | relURL }}) is a
decision to ship one static binary with no runtime to install. That requirement
is met by the **standalone** release: a prebuilt executable per platform with
Node linked in.

Which means the build now downloads and executes a ~110MB binary from the
internet. That is a supply-chain decision whether or not anyone calls it one,
and it deserves a record rather than a line in a Makefile.

There is also a build-ordering constraint that has to be settled now.
`internal/web` embeds the built stylesheet, so `go build` cannot succeed until
`make css` has run. A gate that does not run it produces a build error about a
missing `embed` pattern, in a package that legitimately has no source yet.

## Decision

**Tailwind is the pinned standalone binary, verified against a committed SHA256
of its release manifest. `make check` runs `css` and `templ` before `build`.**

### One committed hash, not four

The release publishes `sha256sums.txt` covering every platform asset. The
Makefile commits **that file's** digest:

```make
TAILWIND_VERSION         := v4.3.3
TAILWIND_MANIFEST_SHA256 := 527b4fcd96950f9ae8f83bbbff27c61e4ff3596cb0b2eb760f9b3516de5d3c56
```

`tools/install-tailwind.sh` verifies in two steps: the manifest against the
committed digest, then the downloaded binary against its line in the verified
manifest. Four committed per-platform digests would be weaker, not stronger —
they would be four values nobody re-derives from upstream, and a compromised
release would ship four matching binaries and four matching hashes.

The alternative — committing the binary, or vendoring the Node toolchain — is
rejected for the reason in 0001: it makes the artifact depend on a machine
state the operator has to reproduce.

### The gate builds the frontend first

`check` is now: format diff, `css`, `templ`, `build`, `vet`, `lint`, `test`.
`build` additionally asserts the stylesheet exists and, if it does not, says
`run: make css` rather than letting the embed fail with `pattern dist: no
matching files found`.

CI installs templ and Tailwind in a **step of their own, before `make ci`**. Not
a separate job, and not folded into the gate: a step failure names the
toolchain, whereas a folded-in failure reports as a vet or lint error against
whatever file happened to import the embed.

### Generated output is not committed

`internal/web/static/dist/` and every `*_templ.go` are gitignored. A generated
file in a diff means the gate was not run — the same rule
[0008]({{ "decisions/0008-forward-only-migrations/" | relURL }}) applies to
shipped migrations, and for the same reason: a committed generated file is a
second copy of something with a source, and it drifts.

## Consequences

- The gate needs the network to bootstrap `.toolbin/`, once per checkout.
  After that it is offline. This is the one part of `make ci` that does.
- `.toolbin/` is repository-local, mirroring the `GOBIN` pattern already in
  `ci.yml`, so a developer's personal `$HOME/go/bin` is never load-bearing.
- Upgrading Tailwind is a two-line diff in the Makefile, and it fails loudly if
  the tag's manifest does not hash to the committed value. A re-published tag
  is a real event; the error message says to update both pins together.
- `make clean` removes `.toolbin/` and the generated assets, so a clean tree
  genuinely is clean.
- A contributor on an unsupported platform cannot build the CSS. The supported
  set is Linux, macOS and Windows on x64 and arm64, which is the whole published
  asset list; the script fails with the uname it did not recognise rather than
  downloading something plausible.

## Alternatives considered

**Node plus `npm install`.** The upstream default, and the only option that
gets automatic updates. Rejected because it adds a second language toolchain to
a project whose entire premise is one static binary, and because `node_modules`
in a Go repository is a supply-chain surface with no version pinning story as
good as a SHA256.

**A pure-Go Tailwind port (`tailwind-go`).** No download, no Node, one binary.
Rejected on the evidence the architecture record already weighed: the port lists
a single importer, and Tailwind v4's CSS-first `@theme` and `@source` are the
features the token layer is built on. Adopting a port to avoid a download would
be trading a verifiable artifact for an unmaintained one.

**Committing the built stylesheet.** Removes the network from the gate and makes
a CSS change a two-step commit. Rejected because the stylesheet is derived
output with a source, and a committed copy is one more thing that can be stale.
It also reintroduces exactly the review problem `check` exists to prevent: a
diff whose generated half nobody reads.

**A Makefile that runs the download on every `make check`.** Always current,
always slow, and it turns a lint fix into a network round-trip. The bootstrap is
a separate `make tailwind` target that is a no-op once `.toolbin/` is populated.

**Verifying only the binary against a committed per-platform digest.** Four
lines instead of one, and it is what most build scripts do. Rejected because
none of the four is checked against upstream, so a bad release is trusted on the
strength of a value this repository wrote down itself.
