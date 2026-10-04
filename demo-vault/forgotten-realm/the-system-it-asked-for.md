---
title: The system it asked for
---

## What `system:` is, and where it is not

It is a **column in the campaigns table**. Not a file in the vault, not a folder name,
not something you can drop into the content root and have the instance notice. A
campaign row says which gameplay system its rules resolve under, and that string is the
whole of it.

Which means the resolution happens somewhere else entirely. Semiplane builds a
**registry of gameplay systems at startup**, and the composition root — one file, the
only place in this repository where anything is registered — is what fills it. There is
no `init()` anywhere, no package-level registry, and no second door: a reader asking
"what does this binary resolve under?" reads that one file and gets the answer.

So there are exactly two things that can happen to a campaign's `system:` at startup:

- **the registry has it** — the campaign's game works, and its content kinds are
  available; or
- **the registry does not have it** — this campaign.

There is no third option, and no halfway one. A system cannot be half-registered.

## Why "not registered" is a state and not a fault

The instinct is to call this a broken row, and the design says otherwise, on purpose:

> A campaign whose plugin was removed or renamed is a real state and not a fault in the
> row.

The row is fine. It says `pathfinder-2e` and that is a true statement about this
campaign. What changed is **the binary**: somebody upgraded, or built without a plugin,
or renamed their plugin's id and did not update their campaigns. The instance is
healthy. Every other campaign on it works. Refusing to boot, or degrading the instance
so that one campaign's absence affects the others, would turn one stale string into an
outage.

That is why the wiki keeps serving, and it is the claim that matters: **the reading
surface and the playing surface fail independently.** Everything on this page you are
reading went through the full read path — redact, parse, render, sanitise, cache — and
not one of those steps ever asked which system this campaign plays.

## And every page here is prose, which is also on purpose

Look at this page's neighbours. None of them declares a `kind:`. They are all prose.

That is not an oversight in the authoring, and it is the same rule one level down. The
kinds a system recognises come from its **data pack**, and this binary has not got the
pack — so the content kinds this campaign would have had are not in the registry here.
A `kind:` naming something this build has never heard of **degrades to prose, in the
index as well as on the page**, and it is inert rather than fatal.

The degradation is deliberate in both directions, and the second half is the one that
matters: a page of a **registered** kind must not degrade. If a kind is silently
falling back to prose everywhere, a reader gets a wiki that has quietly stopped being a
wiki, and the nav tree offers links to things that will not render as what they claim
to be. Both directions are asserted, because satisfying the first while failing the
second is what a registry that knows nothing looks like.

Five kinds survive regardless, because semiplane owns them in every build forever:
a journal, a handout, an index, a token and a scene. They are semiplane's rather than
a system's because a client has to draw a placement without knowing any rules at all —
which is also why a system sharing nothing with 5e needs no client work whatsoever.

This page's own front page is one of the five. It is an `index`, and that resolved.

## The state that was never fingerprinted

There is one more column worth mentioning, because it is the one that *could* have
refused this campaign at boot and deliberately does not.

`ruleset_version` fingerprints what a campaign's saved tabletop state means — the
semantics of its resolution, so that a build which resolves differently cannot silently
resume a state written under different rules. It is emphatically **not** a fingerprint
of your house rules: toggling a house rule must not strand a campaign.

For this campaign that column is **empty**, and the emptiness is chosen rather than
accidental. A fingerprint naming a system that does not exist in this build would be a
fiction written into the very column the resume gate compares — the gate would then be
resuming against a description of rules this process cannot execute. So the seed writes
the empty value, and the empty value means exactly one thing: *state written under no
particular ruleset.*

The resume gate reads that as **`Unfingerprinted`** and reports it **without logging an
error**. There is no drift to report, because there is no fingerprint to have drifted
from. So this campaign does not fail at boot twice over, for the same reason, from two
different subsystems — the refusal happens once, at the dispatch, and once at boot as a
log line, and that is the whole of it.

## What to read next

- [[what-the-game-master-sees]] — the refusal itself, and why the Game Master's browser
  cannot tell them what is missing.
- [[what-the-operator-sees]] — the line that can, printed before the server listens.

And for the other boundary in this vault, on a completely different axis:
`public-post` is a campaign whose reading surface is open and whose game is closed.
This one has both closed. [[/public-post/index|That campaign]] opens the reading
surface and closes the game; this one has a system nobody has installed and a reading
surface that does not care.