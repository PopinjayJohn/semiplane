---
title: What the operator sees
---

## One line, before the server starts listening

Every campaign's `system:` is checked at startup, against the registry the composition
root built. The ones nothing registered are reported:

```
level=ERROR msg=plugin.missing campaign=forgotten-realm system=pathfinder-2e
  detail="no gameplay system is registered with this id, so this campaign cannot
  start a game; its pages still serve"
```

That is the whole of it. One line, per affected campaign, and it carries **the
campaign's slug and the id it wanted** — which are the two facts an operator needs and
the two facts that are safe to name:

- the **slug** is operator-supplied from their own registration, so naming it discloses
  nothing to the operator who is already looking at their own instance;
- the **id** is a column value in their own database.

## It runs before the server listens

This is not a diagnostic that fires when something goes wrong. It is a **boot pass**,
and it runs before the socket is opened, alongside a second pass that checks each
campaign's saved state for ruleset drift.

The argument for boot rather than on-demand is entirely about who reads it. Drift
discovered at boot is a line an operator reads with a coffee in hand. The same
condition discovered by a failed join is a Game Master waiting at a table with nothing
to act on — which is precisely the situation
[[what-the-game-master-sees|this campaign's table]] puts a person in.

And it is a **separate pass from the fingerprint check** rather than a branch of it,
for one reason that is easy to miss: nothing else in the process produces this line. A
registry refusal is only ever seen by a client, and a client is not where an operator
looks.

## It is an error, and not a warning

An operator reading this at boot should not have to decide whether to worry.

The wiki still serves. Every page of this campaign answers 200. The instance is healthy,
nothing else on it is affected, and there is no fire — which is the same reasoning the
resume gate applies to an unreadable ruleset, and the same rule underneath both: **one
campaign is one campaign.**

So it is logged at error level for the reason a Game Master would otherwise find out
mid-evening, and not at warning level because the alternative is an alert rule tuned to
be quiet.

## What is deliberately *not* in the line

**No error text.** Nothing interpolates a message here. That is not fastidiousness: the
id goes in its own structured attribute rather than into the message, because a message
that interpolates a value is a second place the value is spelled, and two spellings
drift.

**No page text, no file contents, no dice results — anywhere.** This is worth stating
because the natural thing to do while debugging is to log the thing that failed, and a
Markdown or YAML parser will quote the line it choked on, which on a wiki page is
routinely the body of a `[!secret]` callout. That is how a secret ends up in a log
aggregator, and it is why the enforcement here is the *shape* of the event attributes
rather than a discipline at each call site: there is no field a page body could be
passed through.

**The reason, when one is in the log at all, is a class.** The typed error classifies
itself as `plugin.missing` — a short, content-free string an alert can match on. That
is the field to build a notification on, and it is the reason the id does not need to
be in the message for anything to work.

## A build with no systems at all

The degenerate version is worth knowing, because it reads as an alarm and is not one.
A binary with **no** gameplay system registered says it once, for the whole instance:

```
level=ERROR msg=plugin.missing
  detail="this build registers no gameplay system, so no campaign can start a game;
  every campaign's wiki still serves"
```

One line rather than one per campaign, because the cause is a single fact about the
binary and not a fact about each row — and a log that names sixty campaigns is a log
nobody reads.

## Why this campaign exists at all

Because this failure is **hard to explain and impossible to see**. Nothing about a wiki
that is working correctly hints that its game is dead, and a reader who does not know to
look for a log line has no way to find out.

So the demo vault keeps one campaign in this state deliberately, and the demo's
completeness gate is wired to **turn red if a Pathfinder plugin is ever registered in
this build**. That is the intended behaviour rather than a bug to suppress: a
demonstration that silently repairs itself has stopped demonstrating anything, and a
green gate over a feature that has been switched off is indistinguishable from a green
gate over a working one.

[[index]] · [[the-system-it-asked-for]] · [[what-the-game-master-sees]]