---
title: The miller's letter
kind: handout
---

## Material for the players

A **handout** is material handed to the players. It is one of the five kinds
semiplane owns, and it is the smallest of them: a handout is a page, it renders, it
links, and that is the whole feature.

The reason it is a kind at all is worth a sentence. A page that is a handout is a page
a Game Master is thinking about differently — it is *for* the players rather than
*about* them, so it gets linked from a table handout menu, printed, or handed out
directly. Declaring it means the intent is in the vault rather than in somebody's
head. What semiplane does not do with it is gate it: a handout is not a secret, and if
one needs hiding it is a collapsed callout on a page rather than a different kind.

---

> **Read aloud, or hand over as written.**
>
> *From the miller of Greyhaven, to whoever is willing to listen at the north gate.*
>
> I have ground at Greyhaven for thirty-one years and I have never once been asked
> what I keep in the north shed. I am asking now, and I am asking badly, because the
> only reason I am still alive to ask is that the fire went out and nobody has had
> the eyes to notice.
>
> Three nights ago a man came down off the east hill in the dark and did not use the
> gate. He came in over the north wall by the old sheep track, which everyone in this
> town believes is impassable, and he went to the shed, and he was inside it long
> enough to empty it and leave it full again.
>
> I have since been down twice and the flour is wrong. It is Greyhaven's flour and it
> has been sifted, and whatever was in that shed is back in the sacks.
>
> I am not a brave man and I am not asking you to fight anyone. I am asking that the
> shed be looked at before the fourth night, and I am asking it at the gate, in
> daylight, where the whole street can hear me ask it.

---

## Notes for the Game Master, not for the handout

Everything below this line is yours. If you are printing the page, delete from here
down — which is a good habit anyway, because the part a reader should not have is the
part that would otherwise be on the same sheet.

> [!secret]- The miller's actual reason  ^millers-reason
> He has been sifting the flour for three nights because the shed is where Greyhaven
> keeps its **sealed** store, and the wax he found on the inside of the north door was
> warm. He did not put it there. He has not told the Wardens because the last person
> he told was his predecessor, and the predecessor is in the river.

The callout is the mechanism; the collapsed state is what a Game Master writes and a
player does not see; and one byte on disk turns it into something the players have
already been told. See [[secrets]].

## What this kind is not

- **Not a permission level.** There is no "handout" access tier. Pages are readable by
  whoever may read the campaign; a handout is a page about which somebody had a
  different intention.
- **Not a document type with different rules.** No separate editor, no separate
  validation, no separate format. It is Markdown with a `kind:` that happens to mean
  something to a person.
- **Not a substitute for a secret.** If it must not be read, it is a callout.

## One thing about callouts in general

`[!secret]` is the **only** callout type semiplane styles. Obsidian has many — note,
warning, tip, question — and in a semiplane page every one of them renders as an
ordinary block quote with its marker text still in it. That is not a missing feature
so much as a boundary: a callout's *appearance* belongs to the product's stylesheet, and
an author cannot reach it, because sanitisation narrows `class` to the handful of
values this pipeline emits. The letter above is a plain block quote for that reason.

Back to [[index]].