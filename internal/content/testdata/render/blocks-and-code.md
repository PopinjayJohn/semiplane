---
title: Block structure, code, and the shapes a page is made of
kind: handout
---

> The beam still turns.
>
> — The keeper's log, volume nine

<details>
<summary>What is behind the door</summary>

A nested list, a nested quote, and a table, all inside a disclosure:

- one
- two

> And a quote inside the disclosure.

</details>

# Code

A fenced block with a language, which is the one place a class value is
author-chosen:

```go
func Light(lamp *Lamp) error {
	return lamp.Wick()
}
```

A fenced block with no language:

```
[[The Lighthouse]]
```

A block whose content would be markup if it were not escaped:

```html
<div class="not-really">this is code, not a div</div>
<script>alert(1)</script>
```

A code span: `[[The Lighthouse]]`, and `{{dice:1d20+5}}`, and `![[chart.png]]`.

An indented block:

    ![[chart.png]]

# Everything at once

A paragraph with [[The Lighthouse]], an embed ![[chart.png]], a
{{dice:1d20+5}} and a {{statblock:Goblin}} in it, then a break, then more.

***

The end.
