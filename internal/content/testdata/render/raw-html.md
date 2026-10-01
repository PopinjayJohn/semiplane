---
title: A page that arrived from someone else's vault
kind: handout
---

# The Drowned Lighthouse

The beam still turns. Nobody has turned it on in eleven years.

Everything from here to the end of the page is raw HTML written by whoever shared
this vault. None of it survives as markup. What survives is the prose around it,
and — where the element's own text was not itself the payload — that text.

A block-level raw HTML element takes its **content** with it, not just its tags.
This is goldmark's own rule — an HTML *block* is one unit, and without
`WithUnsafe()` the whole unit is dropped — and it is worth stating plainly because
it is the one case where a page loses an author's words:

<p onclick="steal()">This sentence is inside a block-level element, and it goes
with the element.</p>

The same construct *inline* keeps its text, because an inline run is only the tags:

An inline <span onclick="steal()">element keeps this sentence</span> in a paragraph.

A script block is the same rule and the most consequential instance:

<script>alert(1)</script>

As is a style block:

<style>body { display: none }</style>

An inline script inside a generic container leaves its *text* behind, because
there is nothing to execute once the tags are gone. The tags are the danger, and
they are gone:

<svg><script>alert(2)</script></svg>

<math><mtext><script>alert(3)</script></mtext></math>

An iframe, an object, a form, a base element, and a comment — each dropped whole:

<iframe src="https://example.com/"></iframe>

<object data="rules.swf"></object>

<form action="/collect"><input name="password" type="password"></form>

<base href="https://evil.example/">

<!--[if IE]><script>alert(4)</script><![endif]-->

An author's raw HTML is not filtered — it is not *interpreted*. goldmark never
turns it into markup, so there is nothing for a sanitiser to remove and nothing
for an author to smuggle. Both of these vanish whole, tags and text alike:

<img src="chart.png" onerror="alert(5)" alt="onerror=alert(5)">

<div onclick="steal()" class="is-gm-only">The chart is in the second drawer.</div>

The markdown forms are how an author writes a picture or a link, and the policy is
what bounds them rather than the author. Both of these survive:

![A coastal chart](chart.png)

[An external link](https://example.com/)

And the second one carries no `target`, no `rel`, and no author-chosen class,
because the policy allows none of the three.

The prose after all of that is intact, and so is the heading above it.
