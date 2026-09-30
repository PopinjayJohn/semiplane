---
title: "0007 — FTS5, unicode61, and deliberately not porter"
description: "An external-content FTS5 table over pages, tokenised without stemming, because TTRPG proper nouns stem badly."
weight: 70
date: "2026-09-30"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

Search runs over `pages.body_plain` — plain text derived from the rendered markdown, with
secret callout content excluded outright (see §5.6.5 of the architecture record; that exclusion
is what stops a search snippet leaking a secret to a player).

The tokenizer is a product decision, not an implementation detail. It changes what a GM finds.

SQLite's FTS5 ships several tokenizers. The default English one applies the Porter stemming
algorithm, which folds related word forms together: `swing` and `swinging` and `swung` become
one term.

## Decision

```sql
CREATE VIRTUAL TABLE pages_fts USING fts5(
  title, body_plain, content='pages', content_rowid='id',
  tokenize='unicode61 remove_diacritics 2'
);
```

**`unicode61`, with diacritics folding, and no `porter` stemmer.**

The reasoning is specific to this domain. TTRPG proper nouns stem *badly*:

- `Svartalfheim` and `svartalf` — a stemmer merges a place name with its adjective.
- `Gandalf` and `Gand` — `gand` is a real English word, so the fold is semantically wrong.
- `Tiamat` and `Tiam` — the fold loses the name.
- `Kenku` and `kenku` fine, but `Rakshasa` and `Rakshas` are not what anyone typed.

Every one of those is a case where a search for the correct spelling returns nothing, and it
returns nothing *silently*. A GM searching for their NPC's name finds nothing and concludes the
search is broken, rather than concluding they are wrong about stemming.

Folding diacritics is the opposite trade and is kept: a GM typing `Svartalfheim` without the
diacritic should find it, because that is a keyboard, not a spelling mistake.

## Consequences

- Plural and past-tense queries do not match. `goblins` will not find `goblin` unless the FTS
  query is written with a prefix (`goblin*`). The query builder uses prefix matching on the last
  term for exactly this reason.
- An external-content FTS table is a **view** over `pages`. It stores no copy of the text.
- External-content tables **cannot be `ALTER`ed**. Any bulk change goes through
  `INSERT INTO pages_fts(pages_fts) VALUES('rebuild')`.
- **Every query joins `campaigns` on visibility.** A bare `SELECT` over FTS returns private page
  titles to an anonymous user. This is asserted by a test, because it is the kind of omission
  that passes review.

## Alternatives considered

**Porter stemming (the FTS5 default).** The conventional choice for prose search, and correct
for it. Rejected because the corpus is not prose — it is a table of named entities, where the
name is the key.

**Trigram tokenizer (`tokenize='trigram'`).** Excellent for substring and partial-word search,
which would fix the plural problem outright. Rejected for v1: it roughly triples index size,
loses ranking quality, and makes `remove_diacritics` unavailable. Worth revisiting if the
prefix-query compromise proves annoying in practice — it would be a migration of a virtual
table, which the architecture record already tells us is drop-and-recreate.

**A separate search engine (Meilisearch, Elasticsearch).** Better ranking, fuzzy matching,
faceting. Rejected decisively: it adds a service to run and back up, which breaks the
one-process premise in [0004]({{ "decisions/0004-single-process-constraint/" | relURL }}), and
it would need its own index anyway, with the same content-versus-database consistency problem
this record exists to avoid.