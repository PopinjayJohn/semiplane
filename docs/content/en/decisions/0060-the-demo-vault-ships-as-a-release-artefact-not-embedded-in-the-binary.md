---
title: "0060 — The demo vault ships as a release artefact, not embedded in the binary"
description: "The demo vault is committed at `demo-vault/`, built by `make demo-artifact` into `semiplane-demo-v<version>.tar.gz`, and published to the GitHub Release beside the binary. The binary embeds none of it, so the vault ships on its own schedule instead of the Go build's — and the price is a version skew between the two, which `semiplane demo seed` refuses when both versions are known."
lede: "Embedding a content artefact in a compiler's output makes prose ship on a compiler's schedule. The alternative is a download, a version number on both sides, and a check between them — and the check only fires when both numbers exist, which is why a development build warns and proceeds."
weight: 5
date: "2026-10-04"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

The demo vault is three campaigns that demonstrate the product: a showcase with
every feature, a public campaign that proves anonymous read, and one whose
gameplay system is deliberately unregistered so the degraded path is visible. It
is committed at `demo-vault/` and it is the **source**; whatever a user
downloads is built from it.

There are two ways to hand that to somebody, and one of them is what the rest of
this repository already does with its browser assets.

**Embed it.** `internal/web` embeds `all:static/dist` and the binary carries
every stylesheet, every vendored byte of PixiJS and Datastar, and every
templ-generated component. The same mechanism could carry `demo-vault/**`, and
`semiplane demo seed` would find its content with no `--root` at all, because
the content root would be the binary itself.

**Publish it.** Build the vault into a tarball, attach the tarball to the
GitHub Release next to the platform binaries, and have the user download and
extract it. The binary does not change when the vault does.

The delivery plan's D7 chose the second, for reasons that are worth separating
because they are not the same size of argument.

### The size argument holds, and today it holds least of all

The architecture record's §16.2 is the observation that started this: a map
image for a tabletop is tens of megabytes, and every user who wants to run
semiplane without ever opening the demo would download all of it. That argument
is real and it is the one that will bite first — a raster map of a published
adventure is a normal thing for a vault to have.

It is also the argument this repository has already spent an effort record
defeating. D19 chose **a hand-authored SVG** declaring a world extent several
times the largest viewport, because panning, zooming and the resize rule are
exercised by the world extent rather than by the image's byte count. The
committed vault is **278 KB** on disk and the artefact is **62,728 bytes**. So
the honest statement of the size argument is: it is a ceiling, not a tax, and
what is embedded today would cost nobody 62 KB. The argument that survives that
is the next two.

### The schedule argument does not care how big the vault is

A vault is content. Content changes because a page reads badly, a wikilink is
wrong, or a feature was added and the demo needs to demonstrate it. None of
those is a reason to cut a Go release, cut a Go release **toolchain bump**, and
put a content fix behind whichever nightly build schedule the project happens
to have. Coupling them means the demo is stale for exactly as long as the
release cycle, and the demo is the first thing a new user meets — so it is the
worst possible artefact to be on the slowest schedule in the repository.

The reverse also holds and is easier to feel: a one-word prose fix in
`demo-vault/greyhaven/notes/…` should be a one-line diff reviewed by whoever
reviews prose, with no compiler anywhere near it.

### The compiler coupling is a category error with a price

Embedding makes a markdown file a **build input**. Then:

- a typo in a page's front matter is a failed `go build`, and the failure is
  reported in Go's error format about an embed pattern rather than about the
  page that is wrong;
- the vault is versioned by the Go toolchain, so `go.mod`'s Go directive becomes
  a constraint on the demo's prose;
- the content is reviewed as source and cannot be corrected by whoever owns the
  content without owning the build;
- and the artefact's provenance becomes the build system's, which means "which
  version is this" stops being a question with a file as its answer.

This repository already decided the adjacent question for third-party browser
assets, and the reasoning transfers directly: committed and digest-pinned rather
than fetched at build time, because a gate that reached the network would make
the outcome depend on a third party being up. The same argument, one level up:
a content artefact fetched by the compiler is content whose bytes depend on when
and where the compiler ran. See
[0052]({{ "decisions/0052-third-party-browser-assets-are-committed-and-digest-pinned/" | relURL }}).

Measured on this tree, the decision holds: a `grep -a` over
`bin/semiplane` for `greyhaven`, `Greyhaven`, `gh-iron-vigil`, `pathfinder-2e`,
`demo-gm` and `The Public Post` finds **none of them**, and the same strings are
present in the tarball. The separation is a fact about the shipped bytes, not an
intention.

## Decision

**The demo vault ships as a release artefact. The binary embeds none of it.**

`demo-vault/` is committed and is the source. `make demo-artifact` builds
`dist/semiplane-demo-v<version>.tar.gz` — the vault tree under one fixed
directory name, `semiplane-demo/`, plus `demo.manifest.yml` — and the release
workflow attaches it to the GitHub Release beside the platform binaries. The
install guide is the download, the extract, and `semiplane demo seed --root`.

Four properties of the artefact are settled here rather than left to the target:

- **`product` is the release it was built for.** The default is read from the
  committed manifest, because that file is already the answer to "which
  semiplane release was this built against" and a copy in the Makefile would be
  a second answer. A release overrides it from the tag, so the manifest inside
  the tarball names the release that published it.
- **The archive is byte-reproducible**, which for a tarball is not free: mtimes,
  uid/gid, `uname`/`gname`, permission bits, entry order and gzip's own header
  all vary between two checkouts. `make demo-artifact` normalises each of them
  and then *asserts* each, because a normalisation nothing checks is a comment.
- **It is not in `check`.** The plan's rule is that `check` touches neither the
  artefact nor the network, and the target's own last assertion reads `make -n
  check` and fails if it has become reachable from there.
- **It requires GNU tar**, and says so, because the flags are GNU spellings and
  the alternative is a wall of "unrecognized option" at release time rather than
  a sentence about the artefact.

`make demo-artifact`'s comment block is the implementation of record for all
four. This record does not restate the flags.

## Consequences

### The price is version skew, and the mitigation is a check that is not yet armed

Two releases must now be in step: the binary, and the artefact holding content
built for it. Nothing about embedding made that problem disappear — it made it
**invisible**, which is worse, because an embedded vault is always exactly the
vault its binary shipped with and cannot disagree with it.

The check is `demo.Check` in `internal/demo/version.go`. Its behaviour, in
full:

- The manifest's `product` is compared with the binary's own version.
- **Both numbers are always in the message**, and neither is shortened away when
  one of them is a placeholder. That is the whole of what makes the refusal
  actionable: an operator is holding an artefact and a binary and needs to know
  which pair, and neither number appears anywhere else — not in the rail's
  version line, which is the product's and not the artefact's.
- A disagreement is `ErrVersionSkew`, a **refusal** that exits non-zero. It
  refuses because the failure it prevents is the quiet one: a vault populated by
  one release, rendering against another, with every page answering 200 and
  nothing in any log to say so.
- A `schema` this build does not read is `ErrSchemaMismatch`, a separate sentinel
  under the same umbrella, and `demo reset` treats the two differently. A
  release mismatch is a reason to reset, so reset reports it and continues. A
  schema mismatch means the campaign and account names in the document may not be
  the ones the artefact created, so a reset that continued would delete whatever
  it *could* read — the worst possible outcome for a command whose job is to
  remove exactly what the seed added.
- A **versionless binary is a warning and proceeds.** `productVersion` is the
  empty string until something stamps it, and `Check` reports
  `(this build carries no version)`, says out loud that the artefact therefore
  cannot be checked against it, and returns no error.

That last bullet is permanent and is the honest limit of this record's mitigation.

### The check was unreachable, and this record is why

`productVersion` in `cmd/server/wiring.go` was **`const productVersion = ""`**. Go's
linker does not rewrite constants, so `go build -ldflags "-X main.productVersion=0.1.0"`
exits 0, changes nothing, and the binary still reported
`(this build carries no version)`. Every artefact/binary pair therefore took the warning
branch, and `ErrVersionSkew` was **unreachable in every build this tree produced** — a
gate whose condition could not be met.

Worse than inert: `-X` against a `const` produces **no error and no warning**, so a
release could stamp a version, watch a clean run, and ship an artefact whose skew check
did nothing. Nothing in the logs would have said so.

`wiring.go`'s own comment predicted the fix and named its trigger: *"The moment the
release workflow stamps a version this becomes a `var`, and nothing else changes."* The
release workflow arrived with this phase, so that moment was the moment, and `productVersion`
is now a `var`. Measured on the same artefact:

| Binary | `-X` stamp | Outcome |
| --- | --- | --- |
| `var`, unstamped | — | exit 0, `cannot be checked against it` |
| `var` | `9.9.9` | **exit 1**, `declares product "0.1.0" and this binary is "9.9.9"` |
| `const` *(before)* | `9.9.9` | exit 0, `cannot be checked against it` |

The third row is the finding: the stamp was accepted and ignored, so the check could not
fire no matter what the release passed it.

**So "the seed refuses skew" is true when both versions are known, and a development build
still warns and proceeds** — which is the right answer for a build that genuinely does not
know its release. Any statement of this mitigation that omits that sentence is overstating
it.

### What the check still cannot catch

Three cases pass it, and none of them is worth pretending otherwise:

- **Two different artefacts both stamped for the same release.** A re-run of the
  workflow over a changed vault produces a different tarball under the same name,
  and the comparison cannot see inside either.
- **An edited vault.** `--root` points at a directory the user has modified. The
  check reads `demo.manifest.yml`, which an edit can also change.
- **A version that is right and content that is not.** The comparison is one
  string. It is a strong signal against the common failure, not a proof of
  anything.

The recovery for all three is the same and is why `demo reset` exists and why it
leaves the vault untouched: reset deletes exactly the rows this artefact created,
and re-extracting the published tarball gives back the bytes it shipped with. The
install guide is written around that loop rather than around a repair.

### The other costs, which are smaller and real

- **The install is two steps and a `--root`.** `semiplane demo seed` cannot infer
  a vault it does not carry, and `--root` must be absolute, refused early,
  because a campaign's content root is derived from it and stored absolute.
- **`make demo-check` and `make demo-artifact` read different trees.**
  `demo-check` gates the committed `demo-vault/` on every push; `demo-artifact`
  stamps a copy and runs at release time. A vault that passes the first and
  fails the second is a release-time failure, which is the right place for it but
  a later one.
- **A stale artefact outlives its release.** The GitHub Release keeps every
  version's tarball, so a user following an old blog post gets a tarball whose
  `product` names a release they no longer have. The skew check is what turns
  that from a mystery into a sentence.

## Alternatives considered

**Embed the vault with `//go:embed`.** Rejected on the schedule and the category
of the input, not on today's 62 KB. It makes every Go release carry content
changes, makes a prose fix wait on a toolchain bump, and turns a front matter typo
into a build failure reported as an embed pattern error. It also deletes the
skew check's whole reason to exist: an embedded vault cannot disagree with the
binary that embeds it, which means the one failure mode this record accepts as
its cost would be structurally impossible — and structurally impossible by making
content changes cost a compiler run.

The strongest argument for it is real and is why this is a decision rather than a
foreclosure: it is **one fewer command**, and `demo seed` with no arguments and
no `--root` is a better product than one that needs a download first. That is
traded against a schedule coupling, and the trade is worth making while the
vault is small and the release cadence is unknown. If the vault ever needs a
raster map and grows past a few megabytes, the first argument becomes
overwhelming and this record should be superseded rather than reinterpreted.

**Ship the vault as a git submodule.** Rejected. It moves the problem rather than
solving it: the user still has to fetch and check out a second repository before
they can run anything, and a submodule's whole purpose — pinning a dependency to
one commit — is a property the tarball already has and states in its own bytes.
It adds a second clone, a second set of credentials and a second thing that can
be at the wrong revision, to deliver the same "here is the vault, byte for byte"
that `curl` and `tar` deliver in two commands. It would also put the vault's
review behind a second repository's contribution rules, which is the wrong way
round for prose that teaches the product.
