---
title: What the Game Master sees
---

## The table opens

This campaign is private and its Game Master is a member of it, so the access gates do
their job exactly as they do for any other table: `GET /c/forgotten-realm/play` is
admitted, the WebSocket upgrades, and the tabletop loads. There is no error page, no
warning banner, and no refusal to *join*.

That is not an oversight in the gate. Joining is a question about membership and
capacity, and both of those are fine. The fault is somewhere the join does not look.

## Every move is refused

Then the Game Master picks up a token and moves it, and the socket answers `server_error`.

And again. And again. Every intent, without exception, from a table that looks entirely
alive — tokens drawn, the map under them, the initiative order where it should be. It
is not a loading state and it is not a disconnect. It is a table that accepts every
gesture and refuses every command, and there is no state in which it does anything
useful.

The refusal happens **per intent**, at the point the intent is dispatched, rather than
once at startup. That placement is the design and not an accident of wiring: the
registry that resolves a `system:` is asked to resolve on every dispatch, and it
answers the same way every time. A startup check instead would have refused to open a
table the Game Master might have wanted to read while they worked out what was wrong —
and would have needed a list of things a *starting* table can do, which is a list
nobody can maintain.

## And the browser is not told which system is missing

Here is the part worth stopping on. The refusal **does** name the id it wanted. It is
a typed error carrying a field, not a formatted string:

> the error is a `*UnknownSystemError` with an `ID` on it, and the id it carries is
> `pathfinder-2e`

A caller holding that error can read the id off it directly, and that is the whole
reason it is a type rather than a message. A caller handed a bare sentinel would have
to already know the id it passed in to reconstruct the sentence — which makes the
answer a property of the caller rather than of the refusal.

**None of that reaches the Game Master.** The frame the socket sends says
`server_error`, and it says nothing else.

This is deliberate, and it is the same rule that keeps page text out of the log. The
text of this refusal is *the reason alone* — `server_error` — because that string
reaches **a browser and a log line**, and a string that reaches both has to be safe in
both. Naming the missing system in the browser would mean a string built out of a
database value, travelling to a client, on a channel a plugin's authors control.

So the honest statement of the boundary is this:

> The product tells the **operator** what is wrong and does not tell the **Game
> Master**. The person at the table gets a table that refuses every move. The person
> with the logs gets a line naming the campaign and the id.

If you are reading this because a table of yours is doing that, the next page is the
one that helps, because the answer you need is not in the table.

## Why not just say it on the page?

Two reasons, and they are different in kind.

**A refusal reason on a page is a UI decision, and the honest one for "the dispatch
could not be resolved" is a generic code.** A player and a Game Master see the same
frame, and a message naming a missing plugin teaches a player nothing they can act on
while teaching a reader of a public campaign quite a lot about the instance.

**The operator-facing answer already exists, and it is in one place.** A separate pass
over the campaigns at startup walks every `system_id` and reports the ones nothing
registered — see [[what-the-operator-sees]]. Duplicating that sentence into the socket
would be a second place the same fact is stated, and the two would drift.

## What this campaign is not

It is not a broken vault, and it is not a bug being demonstrated. Nothing on this
campaign's disk is wrong. Read the rest of the wiki and it is entirely coherent — which
is the point, and is why the pages about it are worth reading rather than skipping to
the log line.

[[index]] · [[what-the-operator-sees]] · [[notes/links-from-a-campaign-without-a-system]]