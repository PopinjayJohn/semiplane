---
title: Editor bookkeeping
---

# This page must never be indexed

`content`'s index skips a leading-dot directory, and this file lives in one. The
demo gate walks the same tree and must reach the same conclusion, or a page the
index does not hold would be a page the gate believes exists.

The rule is the leading dot rather than an explicit list, because the list was
always an enumeration of the same thing: `.git`, `.obsidian`, `.trash` and
`.stfolder` are four spellings of "an editor, a sync client or this process writes
its own bookkeeping here".
