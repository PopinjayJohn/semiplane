# semiplane

A self-hosted, system-agnostic TTRPG wiki and virtual tabletop. One Go binary,
one SQLite file, and campaign content in an Obsidian vault you already keep in
sync.

> **Pre-release.** The repository is a scaffold. The server starts and answers
> `/healthz` and `/readyz`; there is no interface, no campaigns, and no content
> pipeline yet. See the [roadmap](https://popinjayjohn.github.io/semiplane/roadmap/)
> for what exists and in what order the rest arrives.

## Why

The two halves of running a game — the wiki your group writes and the tabletop
you play on — are usually two programs with two models of the same content, and
keeping them in agreement is manual. Here, a page *is* the content: a markdown
file in your vault is simultaneously prose, a stat block, a map, and a handout.

- **Filesystem markdown is the source of truth.** Edit in Obsidian, in the
  browser, or both. Nothing is trapped in a database you cannot read.
- **No rules baked in.** Gameplay systems plug in as compiled-in modules behind
  one interface, so a new one costs no client work.
- **One process, one file.** No external services, no account to create anywhere.

## Documentation

**[popinjayjohn.github.io/semiplane](https://popinjayjohn.github.io/semiplane/)**

- [Install](https://popinjayjohn.github.io/semiplane/install/) — run it
- [Concepts](https://popinjayjohn.github.io/semiplane/concepts/) — the vocabulary, and why "session" is not it
- [Operating](https://popinjayjohn.github.io/semiplane/operating/) — backup, upgrades, the single-instance rule
- [Security](https://popinjayjohn.github.io/semiplane/operating/security/) — the trust boundary
- [Design records](https://popinjayjohn.github.io/semiplane/design/) — the architecture and UI specifications, published verbatim

## Run it

```bash
git clone https://github.com/PopinjayJohn/semiplane.git
cd semiplane
make run          # http://localhost:8080
make ci           # the gate: fmt, build, vet, lint, test -race
make site-serve   # the docs site, with live reload
```

## Contributing

Read [`AGENTS.md`](AGENTS.md) first — it is the operational contract for agents
and contributors alike, and `make ci` is the gate that decides whether a change
is finished. The [contributing guide](https://popinjayjohn.github.io/semiplane/contributing/)
covers the process and the label vocabulary.

## Licence

[MIT](LICENSE).
