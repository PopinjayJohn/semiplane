---
title: Prose
---

# Prose

An ordinary page. It has front matter — a title — and no `kind`, and a page with
no kind renders as prose, which is the same answer a page with a kind this build
does not recognise gets.

## Links

A `[[wikilink]]` is written with double square brackets and resolves against the
campaign's own index first. [[prose]] links to itself here, and
[[index]] is a link to the campaign index by base name, from a page in the
campaign root.

An alias is the part after the pipe: [[index|the campaign index]] says one thing
and displays another. An in-page reference like [[#Links]] names a heading on this
page and no page at all.

A link is written without its extension and resolves either way: [[index.md]] and
[[index]] are the same page.
