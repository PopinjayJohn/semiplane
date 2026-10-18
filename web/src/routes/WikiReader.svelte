<script lang="ts">
  /*
   * routes/WikiReader.svelte — wiki page reader (slice S3).
   *
   * Props: path — vault-relative note path, e.g. "campaign/intro.md".
   *
   * Fetches GET /api/v1/notes/{path} with ?role= from the session store,
   * re-fetching whenever the path or the session role changes. Renders the
   * server-trusted fragment HTML, decorates wikilinks for client-side
   * navigation, and renders per-GM-block reveal UI gated by session role.
   *
   * Strict-CSP clean: no inline handlers, no style= attributes, {@html} only
   * for server-trusted fragment HTML, every interactive element carries
   * data-testid.
   */
  import { session } from '../lib/stores/session.svelte.ts';
  import { getNote, postNote, search } from '../lib/api/notes.js';
  import { reveal } from '../lib/api/dice.js';
  import { APIError } from '../lib/api/client.js';
  import { sse } from '../lib/stores/sse.svelte.ts';
  import { announce } from '../lib/announce.ts';
  import type { NoteDoc, SearchHit } from '../lib/api/types.js';

  let { path } = $props<{ path: string }>();

  let note = $state<NoteDoc | null>(null);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let notFound = $state(false);
  let role = $derived(session.role);

  let q = $state('');
  let hits: SearchHit[] = $state([]);
  let searching = $state(false);
  let searchError = $state<string | null>(null);
  let createResult = $state<string | null>(null);
  let createError = $state<string | null>(null);

  type RevealState = { html: string | null; revealed: boolean };
  let revealState = $state<Map<string, RevealState>>(new Map());
  let revealError = $state<string | null>(null);

  let blockIds = $derived(scanGmBlockIds(note?.content ?? ''));
  let page = $derived(note?.path);

  /* --- fetching ---

     ETag: the etag is displayed in wiki-meta. Conditional re-fetches with
     If-None-Match (handled server-side, openapi.yaml) are best-effort here
     because getNote does not yet accept request headers.
   */

  let loadGeneration = 0;

  async function loadNote() {
    const gen = ++loadGeneration;
    const p = path;
    const r = role ?? undefined;
    loading = true;
    error = null;
    try {
      const doc = await getNote(p, r);
      if (gen !== loadGeneration) return;
      note = doc;
      notFound = false;
      loading = false;
    } catch (err) {
      if (gen !== loadGeneration) return;
      loading = false;
      if (err instanceof APIError && err.status === 404) {
        notFound = true;
        note = null;
      } else {
        error = err instanceof Error ? err.message : 'Failed to load note';
      }
    }
  }

  $effect(() => {
    loadNote();
  });

  /* --- wikilink decoration ---

     Anchors inside the rendered fragment whose href points to a wiki route
     (/wiki/... or /notes/... as emitted by the server renderer) are
     intercepted: preventDefault, then pushState + dispatch a popstate event
     so App.svelte — the single source of truth for pathname — re-renders
     WikiReader with the new path. No reload.
   */

  function wikiTarget(href: string): string | null {
    if (href.startsWith('#')) return null;
    let pathname: string;
    try {
      pathname = new URL(href, window.location.origin).pathname;
    } catch {
      return null;
    }
    if (pathname.startsWith('/wiki/'))
      return pathname.slice('/wiki/'.length).replace(/\.md$/, '');
    if (pathname.startsWith('/notes/'))
      return pathname.slice('/notes/'.length).replace(/\.md$/, '');
    return null;
  }

  function navigateTo(target: string) {
    window.history.pushState(null, '', `/wiki/${target}`);
    window.dispatchEvent(new PopStateEvent('popstate'));
  }

  $effect(() => {
    const n = note;
    const container = document.querySelector<HTMLElement>('[data-testid="wiki-fragment"]');
    if (!n || !container) return;
    for (const a of container.querySelectorAll<HTMLAnchorElement>('a[href]')) {
      if (a.dataset.wikiDecorated === '1') continue;
      const target = wikiTarget(a.href);
      if (target === null) continue;
      a.dataset.wikiDecorated = '1';
      a.setAttribute('data-testid', 'wiki-link');
      a.addEventListener('click', (event) => {
        event.preventDefault();
        navigateTo(target);
      });
    }
  });

  /* --- 404 fallback: search + create --- */

  async function handleSearch() {
    const query = q.trim();
    if (!query) return;
    searching = true;
    searchError = null;
    hits = [];
    try {
      const res = await search(query);
      hits = res.hits;
    } catch (err) {
      searchError = err instanceof Error ? err.message : 'Search failed';
    } finally {
      searching = false;
    }
  }

  async function handleCreate() {
    createResult = null;
    createError = null;
    try {
      await postNote(path, '', '', role ?? undefined); // empty hash = create
      createResult = `Page “${path}” created.`;
      await loadNote();
    } catch (err) {
      createError = err instanceof Error ? err.message : 'Create failed';
    }
  }

  /* --- GM block reveal ---

     Subscribes to the shared SSE store for block_revealed events
     {page, blockId, html}; events matching a rendered block paint event.html
     into its reveal-target and announce via aria-live. Painted content is
     never stripped client-side. (S4: one SSE connection shared across pages.)
   */

  function blockState(blockId: string): RevealState {
    return revealState.get(blockId) ?? { html: null, revealed: false };
  }

  async function handleReveal(blockId: string) {
    revealError = null;
    try {
      await reveal(page ?? '', blockId);
      revealState.set(blockId, { ...blockState(blockId), revealed: true });
    } catch (err) {
      revealError = err instanceof Error ? err.message : 'Reveal failed';
    }
  }

  $effect(() => {
    const p = page;
    if (!p) return;
    const unsub = sse.on('block_revealed', (event) => {
      if (event.type !== 'block_revealed') return;
      if (event.page !== p) return;
      const state = revealState.get(event.blockId);
      if (!state) return;
      revealState.set(event.blockId, {
        ...state,
        html: event.html,
        revealed: true,
      });
      announce(`GM revealed “${event.blockId}” on ${p}`);
    });
    return () => unsub();
  });

  /* --- gm block enumeration from raw content ---

     note.content carries the raw Markdown (:::gm fences included) for both
     roles; only the fragment is role-rendered. Block ids (gm-0, gm-1, …)
     are derived from fence order so GM and player clients agree on them.
   */

  function scanGmBlockIds(content: string): string[] {
    const ids: string[] = [];
    let inGm = false;
    let index = 0;
    for (const line of content.split(/\r?\n/)) {
      const trimmed = line.trimStart();
      if (!inGm) {
        if (
          trimmed === ':::gm' ||
          trimmed.startsWith(':::gm ') ||
          trimmed.startsWith(':::gm\t')
        ) {
          ids.push(`gm-${index++}`);
          inGm = true;
        }
      } else if (trimmed === ':::') {
        inGm = false;
      }
    }
    return ids;
  }
</script>

<section class="wiki-reader">
  {#if loading}
    <p data-testid="wiki-loading" role="status">Loading page…</p>
  {:else if error}
    <div class="wiki-state">
      <p data-testid="wiki-error" class="wiki-error" role="alert">{error}</p>
      <button
        data-testid="wiki-retry"
        type="button"
        onclick={loadNote}
        class="wiki-button"
      >
        Retry
      </button>
    </div>
  {:else if notFound}
    <section data-testid="wiki-404" class="wiki-404">
      <p data-testid="wiki-404-message">Page “{path}” not found.</p>
      <div class="wiki-search">
        <label for="wiki-search-input">Search the vault</label>
        <input
          id="wiki-search-input"
          data-testid="wiki-search-input"
          type="search"
          bind:value={q}
          placeholder="Search…"
        />
        <button
          data-testid="wiki-search-btn"
          type="button"
          disabled={searching}
          onclick={handleSearch}
          class="wiki-button"
        >
          {searching ? 'Searching…' : 'Search'}
        </button>
      </div>
      {#if searchError}
        <p data-testid="wiki-search-error" role="alert">{searchError}</p>
      {/if}
      {#if hits.length}
        <ul data-testid="wiki-search-results">
          {#each hits as hit}
            <li>
              <a
                data-testid="wiki-link"
                href={`/wiki/${hit.path}`}
                onclick={(event) => {
                  event.preventDefault();
                  navigateTo(hit.path);
                }}
                class="wiki-link"
              >
                {hit.path}
              </a>
              <p>{hit.snippet}</p>
            </li>
          {/each}
        </ul>
      {/if}
      <button
        data-testid="wiki-create-btn"
        type="button"
        onclick={handleCreate}
        class="wiki-button"
      >
        Create page “{path}”
      </button>
      {#if createResult}
        <p data-testid="wiki-create-result">{createResult}</p>
      {/if}
      {#if createError}
        <p data-testid="wiki-create-error" role="alert">{createError}</p>
      {/if}
    </section>
  {:else if note}
    <h1 data-testid="wiki-page" class="wiki-page-title">{note.path}</h1>
    <div data-testid="wiki-fragment" class="wiki-fragment">
      {@html note.fragment}
    </div>
    <div data-testid="wiki-meta" class="wiki-meta">
      {#if note.etag}
        <p data-testid="wiki-etag" class="wiki-etag">ETag: {note.etag}</p>
      {/if}
      {#each Object.entries(note.meta ?? {}) as [key, value]}
        <p data-testid="wiki-meta-entry">{String(key)}: {String(value)}</p>
      {/each}
    </div>
    <div data-testid="wiki-backlinks" class="wiki-backlinks">
      <h3 class="wiki-backlinks-title">Backlinks</h3>
      <ul>
        {#each note.backlinks as backlink}
          <li>
            <a
              data-testid="wiki-backlink"
              href={`/wiki/${backlink}`}
              onclick={(event) => {
                event.preventDefault();
                navigateTo(backlink);
              }}
              class="wiki-link"
            >
              {backlink}
            </a>
          </li>
        {/each}
      </ul>
      {#if note.backlinks.length === 0}
        <p>No backlinks.</p>
      {/if}
    </div>
    {#each blockIds as blockId (blockId)}
      <section data-testid="gm-block" class="gm-block" aria-label="GM block">
        {#if role === 'gm'}
          {#if blockState(blockId).revealed}
            <p data-testid="gm-revealed" class="gm-revealed">Revealed to players.</p>
          {:else}
            <button
              data-testid="reveal-btn"
              type="button"
              onclick={() => handleReveal(blockId)}
              class="wiki-button wiki-button--reveal"
            >
              Reveal to players
            </button>
          {/if}
          {#if revealError}
            <p data-testid="gm-reveal-error" role="alert">{revealError}</p>
          {/if}
          <div
            data-testid="reveal-target"
            class="reveal-target"
            aria-live="polite"
          >
            {#if blockState(blockId).html}
              {@html blockState(blockId).html!}
            {/if}
          </div>
        {:else}
          {#if blockState(blockId).html}
            <div
              data-testid="reveal-target"
              class="reveal-target"
              aria-live="polite"
            >
              {@html blockState(blockId).html!}
            </div>
          {:else}
            <p data-testid="reveal-pending" class="reveal-pending">
              GM content is hidden until a GM reveals it.
            </p>
            <div data-testid="reveal-target" class="reveal-target"></div>
          {/if}
        {/if}
      </section>
    {/each}
  {/if}
</section>

<style>
  .wiki-reader {
    display: flex;
    flex-direction: column;
    gap: 16px;
    max-width: 900px;
  }
  .wiki-page-title {
    margin: 0 0 8px 0;
    font-size: 1.5rem;
    font-weight: 700;
    color: var(--sp-fg);
    word-break: break-word;
  }
  .wiki-fragment {
    line-height: 1.7;
    color: var(--sp-fg);
  }
  .wiki-state {
    display: flex;
    flex-direction: column;
    gap: 10px;
    color: var(--sp-fg-muted);
  }
  .wiki-error {
    margin: 0;
    color: var(--sp-danger);
  }
  .wiki-meta {
    margin-top: 12px;
    padding: 10px 14px;
    border: 1px solid var(--sp-border);
    border-radius: var(--sp-radius-sm);
    background: var(--sp-bg-raised);
    font-size: 0.875rem;
    color: var(--sp-fg-muted);
  }
  .wiki-meta p {
    margin: 4px 0;
  }
  .wiki-etag {
    font-family: var(--sp-mono);
  }
  .wiki-backlinks {
    margin-top: 12px;
  }
  .wiki-backlinks-title {
    margin: 0 0 6px 0;
    font-size: 1rem;
    color: var(--sp-fg);
  }
  .wiki-backlinks ul {
    margin: 0;
    padding-left: 18px;
  }
  .wiki-backlinks li {
    margin-bottom: 4px;
  }
  .wiki-search {
    display: flex;
    flex-direction: column;
    gap: 8px;
    margin-top: 10px;
  }
  .wiki-search label {
    font-size: 0.875rem;
    color: var(--sp-fg-muted);
  }
  .wiki-search input {
    padding: 8px 10px;
    border: 1px solid var(--sp-border);
    border-radius: var(--sp-radius-sm);
    background: var(--sp-bg-raised);
    color: var(--sp-fg);
    font: inherit;
    font-size: 0.9375rem;
  }
  .wiki-search input:focus-visible {
    outline: 3px solid var(--sp-accent);
    outline-offset: 2px;
    border-color: var(--sp-accent);
  }
  .wiki-button {
    display: inline-flex;
    align-items: center;
    padding: 8px 14px;
    border: 1px solid var(--sp-border);
    border-radius: var(--sp-radius-sm);
    background: var(--sp-bg-raised);
    color: var(--sp-fg);
    font: inherit;
    font-size: 0.9375rem;
    cursor: pointer;
    min-height: 40px;
  }
  .wiki-button:focus-visible {
    outline: 3px solid var(--sp-accent);
    outline-offset: 2px;
  }
  .wiki-button--reveal {
    background: var(--sp-accent);
    color: var(--sp-accent-contrast);
    border-color: var(--sp-accent);
  }
  .wiki-button--reveal:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }
  .wiki-link {
    color: var(--sp-accent);
    text-decoration: underline;
  }
  .gm-block {
    border: 1px solid var(--sp-secret-border);
    border-radius: var(--sp-radius-sm);
    padding: 12px;
    background: var(--sp-bg-overlay);
    margin-top: 12px;
    color: var(--sp-fg);
  }
  .reveal-pending {
    margin: 0 0 8px 0;
    color: var(--sp-fg-muted);
  }
  .reveal-target {
    margin-top: 8px;
    border-top: 1px dashed var(--sp-border);
    padding-top: 8px;
  }
  .gm-revealed {
    margin: 0;
    color: var(--sp-ok);
    font-size: 0.875rem;
  }
  .wiki-404 {
    color: var(--sp-fg-muted);
  }
</style>
