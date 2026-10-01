---
title: Links, and what happens to a bad one
kind: handout
---

A [safe link](https://example.com/atlas) keeps its address.

A [relative link](/c/greyhaven/wiki/Some%20Page) keeps its address too.

An [in-page link](#the-drowned-lighthouse) points at a heading on this page.

A [hostile link](javascript:alert('xss')) loses its address and keeps its text.

A [second hostile link](data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==)
loses its address as well.

A [file link](file:///etc/passwd) is not a thing a page may offer.

![an image with a hostile source](javascript:alert(1))

The sentence survives even though the destinations do not, which is the point:
a GM whose sync pulled in a hostile link wants the prose to still read.
