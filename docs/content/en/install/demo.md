---
title: "The demo vault"
description: "Download the demo vault, seed an instance from it, read the showcase campaign, and put it back. Every command on this page was run to produce it."
lede: "The demo is three campaigns that demonstrate the product against itself: a showcase, a public one, and one whose gameplay system is deliberately missing. It installs in three commands and is removed by a fourth."
weight: 11
---

The demo vault is three campaigns, and installing it is one download, one extract
and one command. It is worth having on any instance you are evaluating, because it
is the fastest way to see the boundaries as well as the features: one campaign is
public, one is private, and one names a gameplay system this build does not have.

The vault is also its own tutorial. Once you are in, the showcase campaign's index
page introduces every feature in the order a newcomer meets them, and the prose
does the teaching — so this page covers getting in, and the campaign covers
everything after that.

## What you need

A `semiplane` binary **and** the demo artefact, from the same release. The artefact
is not embedded in the binary and there is no command that fetches it; see
[0060]({{ "decisions/0060-the-demo-vault-ships-as-a-release-artefact-not-embedded-in-the-binary/" | relURL }})
for why, and for what happens when the two do not match.

Building from source works too, and the artefact is built by the same repository:

```bash
make demo-artifact     # writes dist/semiplane-demo-v<version>.tar.gz
```

## Install

Run these in an empty directory of your own. Nothing outside it is touched.

### 1. Download

The asset name carries the version, because the release publishes one artefact per
release and there is no "latest" name to fall back on:

```bash
VERSION=0.1.0
curl -fsSL -o semiplane-demo.tar.gz \
  "https://github.com/PopinjayJohn/semiplane/releases/download/v${VERSION}/semiplane-demo-v${VERSION}.tar.gz"
```

`-f` matters more than it looks: without it `curl` writes the error page into
`semiplane-demo.tar.gz` and the failure surfaces two commands later as a gzip
error. `gh release download` is equivalent if you have it.

### 2. Extract

```bash
tar -xzf semiplane-demo.tar.gz
ls semiplane-demo
```

```
demo.manifest.yml
forgotten-realm
greyhaven
public-post
```

One directory, `semiplane-demo/`, and inside it `demo.manifest.yml` plus one
directory per campaign. The name is **fixed, not versioned**, so
`--root ./semiplane-demo` is the same path at every release and this page can
quote it without a placeholder.

Every mtime is the epoch and every uid/gid is zero. That is deliberate — the
artefact is byte-reproducible, so two builds of the same vault on two machines
are the same file, and a digest you record means something.

### 3. Seed

```bash
semiplane demo seed --root "$PWD/semiplane-demo"
```

Which prints, on **stdout** and once:

```
Demo account password, printed once and stored nowhere in plain text:
  <the password this run printed>

Registered campaign "greyhaven".
Registered campaign "public-post".
Registered campaign "forgotten-realm".
Created account "demo-gm".
Created account "demo-player".

Seeded. Start the server and sign in with a demo account.
```

**Copy that password into your password manager now.** It is drawn at random, it
is stored only as a PBKDF2 hash, and it is never printed again — not by
`demo seed`, not by `demo reset`, not by any log line. A seed that fails partway
still prints it, because a half-seeded instance holds an account you would
otherwise have no way to sign in to.

Everything else — the plugin-registration log line and the warnings below — goes
to **stderr**, so this captures the credential and the report of what it did, and
no log noise with it:

```bash
semiplane demo seed --root "$PWD/semiplane-demo" > seed.txt
```

Two lines of stderr are expected and are not faults:

- `the binary is (this build carries no version), so the demo artefact built for
  "0.1.0" cannot be checked against it` — the skew check needs **both** versions,
  and a binary built outside a release workflow does not know its own. It
  proceeds, and says so. [0060]({{ "decisions/0060-the-demo-vault-ships-as-a-release-artefact-not-embedded-in-the-binary/" | relURL }}#consequences)
  records that the check's refusal branch is currently unreachable for that reason.
- `no gameplay system is registered for forgotten-realm, so those campaigns serve
  their wiki and refuse to start a game` — that is the third campaign doing the
  job it exists for.

#### The demo account is a Game Master and nothing more

`demo-gm` is the **Game Master of the three seeded campaigns**. It is **never an
instance administrator**. It cannot register a campaign, cannot create or manage
users, and cannot see any campaign the artefact did not create.

That is deliberate, and it is the reason a demo vault is safe to download and paste
into a public issue: an account that could enumerate and administer every other
campaign on your instance would make the artefact the sharpest thing you ever
published. If you want your own instance administration, that is
[`semiplane admin create`]({{ "install/" | relURL }}), and it is a separate account
you create on purpose.

If you also want to see the same URLs as a player, sign in as `demo-player`. Same
pages, minus anything hidden — and on the pages that carry a secret, fewer bytes
rather than different ones.

#### Where the vault lives

The content root of each campaign is `<root>/<slug>`, which is the directory you
just extracted. `SEMIPLANE_CONTENT_ROOT_BASE` **does not apply to the demo** and
you do not need to set it: the seed passes the extracted directory to the same
registrar `admin campaign add` uses, and that registrar derives the root from the
path it is given. Setting it changes nothing here.

The one variable worth setting is the database, because its default is relative:

```bash
export SEMIPLANE_DATABASE_URL="file:$PWD/semiplane.db"
```

Left alone, it is `semiplane.db` **in your current directory**, which means the
database moves when you `cd`. The demo refuses a relative `--root` for exactly
this class of reason and does so before reading any file, because a campaign's
content root is stored absolute and a relative one would name a different vault
after a restart from somewhere else.

## Open

```bash
semiplane serve          # http://localhost:8080
```

Sign in with `demo-gm` and the password the seed printed. The three campaigns are
listed, and the URLs are real:

| URL | As `demo-gm` | As an anonymous visitor |
|---|---|---|
| `/c/greyhaven/wiki/index` | 200 | 404 |
| `/c/greyhaven/play` | 200 | 404 |
| `/c/public-post/wiki/index` | 200 | 200 |
| `/c/forgotten-realm/wiki/index` | 200 | 404 |
| `/c/forgotten-realm/play` | served; **the game refuses to start** | 404 |

`forgotten-realm` is the interesting one. Its pages answer 200 and its game does
not start, because its `system` is `pathfinder-2e` and no plugin is registered
under that id. The refusal names the id it wanted — in the operator's log, not in
the reader's document, which is where that string belongs:

```json
{"level":"ERROR","msg":"plugin.missing","campaign":"forgotten-realm",
 "system":"pathfinder-2e",
 "detail":"no gameplay system is registered with this id, so this campaign cannot start a game; its pages still serve"}
```

Do not "fix" that campaign by registering a Pathfinder plugin. It is the only
demonstration of that failure mode.

## Reset

To put the instance back:

```bash
semiplane demo reset --root "$PWD/semiplane-demo"
```

```
Removed the campaign "greyhaven" and its state.
Removed the campaign "public-post" and its state.
Removed the campaign "forgotten-realm" and its state.
Removed the account "demo-gm".
Removed the account "demo-player".
Removed the account "demo-gm".
Removed the account "demo-player".

The vault is untouched. Extract the artefact again to start over.
```

The manifest declares two accounts and each is listed twice. That is verbatim
output, not a formatting quirk to shrug at: `internal/demo/reset.go` seeds
`ResetResult.Accounts` from the pre-flight plan and then appends each deleted
username to it again in the delete loop, so the list holds every username twice.
Nothing is deleted twice — the rows are gone either way — and the fix is one line
in that file. Treat this block as the thing to update when it is.

Reset deletes database rows and **never writes to the vault**, which is the part
worth checking rather than reading. This is the check:

```bash
find semiplane-demo -type f | LC_ALL=C sort | xargs sha256sum | sha256sum
```

Run it after extracting, after seeding, and after resetting, and all three print
the same digest:

```
c32de873f2b608604cb20dab71c7d779ac64fe1240c5049499a9cfc37af203c8  -
```

So the vault's forty files are byte-identical before the seed, during it and after
the reset. Re-extracting is therefore always enough to start over, and there is
no state in the vault to clean up.

There is one honest caveat. The seed **does** change the vault once: it tightens
each campaign's directory to mode `0700`, from the `0755` that extraction leaves,
because a campaign's content root is a security boundary and a directory any
account on the host can read is a campaign any account on the host can read. It
changes nothing else, and reset does not put the mode back — which is correct, and
`tar -xzf` over the top of an existing directory will not either. Delete the
directory and extract again if you want it back.

Re-seeding after a reset works, and draws a **new** password:

```bash
semiplane demo seed --root "$PWD/semiplane-demo"    # prints a new password
```

## A second seed is refused

Running the seed twice is the mistake worth pre-empting, so it is worth seeing
what it says. It refuses, exits non-zero, and names both sides of the comparison
along with the exact command that undoes it:

```
semiplane: this instance is already seeded: the artefact declares product "0.1.0"
and this binary is "(this build carries no version)", and this instance already has
the campaign greyhaven, the campaign public-post, the campaign forgotten-realm, the
account demo-gm, the account demo-player; `semiplane demo reset --root …/semiplane-demo`
removes exactly what this artefact created and leaves the vault untouched
```

Read the two versions as what they are. `0.1.0` is the release the artefact was
built for. The binary's half is the literal string
`(this build carries no version)` — which is **not** a version and is not being
compared to anything. A binary that knows its release names a real number here
instead, and a genuine mismatch is refused rather than warned about.

The refusal lists the accounts it is about to leave alone, which is the useful
half: it tells you the instance already holds them, so the seed did not half-run
and there is nothing to clean up by hand.

You will also see the `Demo account password, printed once…` header with **nothing
under it**, immediately before the error. The password is printed on every seed
whether or not the seed succeeded — the point being that a seed which fails
*partway* has already created an account you need its password for — and here
nothing was created, so there is nothing to print. **That line is not your
password.** The one that is, is the one your first successful seed printed.

## If a command does not do what this page says

**`demo seed --help` is not a help form.** The flag grammar accepts `--root`,
`--password` and nothing else, so `--help` is an unknown flag and exits 1. The
usage text is on the subcommand group:

```bash
semiplane demo help
```

Likewise `semiplane help` for the top-level commands and `semiplane admin help`
for the account ones. (`semiplane admin create --help` is the same story: the
admin parser wants a value for every flag it recognises.)

**A relative `--root` is refused before anything is read**, with the reason:

```
semiplane demo: demo reset: --root "semiplane-demo" is not absolute; every
campaign's content root is derived from it and stored absolute, so a relative
root would name a different vault after a restart from a different directory
```

`"$PWD/…"` is not a workaround, it is the reason.

**The seed and the server cannot run at once.** Both hold the process's
single-instance slot, so stop the server before seeding or resetting — that is
also why they are subcommands rather than flags. See
[0004]({{ "decisions/0004-single-process-constraint/" | relURL }}).

**A stale artefact.** The GitHub Release keeps every version, so a link from an
old post gives you a tarball whose `product` names a release you do not have. The
error message says so, and the fix is to extract the artefact published by the
release whose binary you are running.
