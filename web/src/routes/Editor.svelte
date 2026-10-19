<script lang="ts">
  /*
   * routes/Editor.svelte — TipTap editor route (slice S5).
   *
   * Dynamically imported by App.svelte so @tiptap (~118 KB gzipped) lives in
   * its own chunk, off the shell bundle. Edits a vault note in Markdown via
   * @tiptap/core + starter-kit + Markdown extension + three custom nodes
   * (dice button, gm block, statblock), each implementing all three markdown
   * hooks so content round-trips through editor.getMarkdown().
   *
   * Upsert with hash guard: POST /api/v1/notes/{path} {content, hash}; a
   * stale hash → 409 → conflict banner with "Reload latest" (re-read + retry).
   *
   * Strict-CSP clean: no inline handlers, no style= attributes, --sp-* tokens
   * only, every interactive element carries data-testid.
   */
  import { onMount, onDestroy } from 'svelte';
  import { Editor } from '@tiptap/core';
  import StarterKit from '@tiptap/starter-kit';
  import { Markdown } from '@tiptap/markdown';
  import { customNodes } from '../lib/extensions/index.js';
  import { splitFrontmatter, joinFrontmatter } from '../lib/editor-frontmatter.js';
  import { getNote, postNote } from '../lib/api/notes.js';
  import { APIError } from '../lib/api/client.js';
  import { session } from '../lib/stores/session.svelte.ts';
  import { announce } from '../lib/announce.ts';

  let { path } = $props<{ path: string }>();
  let el = $state<HTMLDivElement | null>(null);
  let editor: Editor | null = null;

  let hash = $state<string>('');
  let loading = $state(true);
  let saving = $state(false);
  let conflict = $state(false);
  let status = $state<'idle' | 'saved' | 'error' | null>(null);
  let statusMessage = $state('');

  /* Frontmatter is held out of TipTap entirely: the Markdown extension has
   * no YAML concept and would parse the fences as markup (mangling the note
   * on every save). The front slice re-attaches verbatim at save time. The
   * separator/trailing blank lines around the body are likewise not part of
   * TipTap's block model (getMarkdown drops them), so they are remembered at
   * load and restored at save — a save of an untouched note is byte-exact. */
  let frontmatter = $state<string>('');
  let bodyLeadBlank = $state(false); // body opened with a blank separator line
  let docTrailingNL = $state(false); // note ended with a newline

  function noteToEditorBody(content: string): string {
    const split = splitFrontmatter(content);
    frontmatter = split.frontmatter;
    bodyLeadBlank = split.body.startsWith('\n');
    docTrailingNL = content.endsWith('\n');
    return split.body;
  }

  function editorBodyToNote(body: string): string {
    let out = joinFrontmatter(
      frontmatter,
      bodyLeadBlank && !body.startsWith('\n') ? '\n' + body : body,
    );
    if (docTrailingNL && !out.endsWith('\n')) out += '\n';
    return out;
  }

  async function load() {
    const doc = await getNote(path, session.role ?? undefined);
    hash = doc.etag;
    if (editor) {
      editor.commands.setContent(noteToEditorBody(doc.content), {
        contentType: 'markdown',
      });
    }
    loading = false;
    conflict = false;
    status = null;
  }

  onMount(async () => {
    editor = new Editor({
      element: el,
      extensions: [StarterKit, Markdown, ...customNodes],
      editable: true,
      autofocus: false,
    });
    await load();
  });

  onDestroy(() => {
    editor?.destroy();
    editor = null;
  });

  async function save() {
    if (!editor) return;
    saving = true;
    conflict = false;
    status = null;
    try {
      const content = editorBodyToNote(editor.getMarkdown());
      const doc = await postNote(path, content, hash);
      hash = doc.etag;
      status = 'saved';
      statusMessage = 'Saved.';
      announce(`Saved ${path}`);
    } catch (err) {
      if (err instanceof APIError && err.status === 409) {
        conflict = true;
        statusMessage =
          'The page was changed by someone else. Reload to see the latest version, then save again.';
      } else {
        status = 'error';
        statusMessage = err instanceof Error ? err.message : 'Save failed';
      }
    } finally {
      saving = false;
    }
  }

  async function reload() {
    const doc = await getNote(path, session.role ?? undefined);
    hash = doc.etag;
    if (editor) {
      editor.commands.setContent(noteToEditorBody(doc.content), {
        contentType: 'markdown',
      });
    }
    conflict = false;
    status = null;
  }
</script>

<section data-testid="editor-page" class="editor-page">
  {#if loading}
    <p data-testid="editor-loading" role="status">Loading editor…</p>
  {:else}
    <div bind:this={el} data-testid="editor" class="editor"></div>
    <div class="editor-controls">
      <button
        data-testid="editor-save"
        type="button"
        disabled={saving}
        onclick={save}
        class="editor-button"
      >
        {saving ? 'Saving…' : 'Save'}
      </button>
      {#if conflict}
        <button
          data-testid="editor-reload"
          type="button"
          onclick={reload}
          class="editor-button editor-button--outline"
        >
          Reload latest
        </button>
      {/if}
    </div>
    {#if conflict}
      <div data-testid="editor-conflict" role="alert" class="editor-state editor-state--conflict">
        <p>{statusMessage}</p>
      </div>
    {:else if status === 'saved'}
      <div data-testid="editor-saved" class="editor-state editor-state--saved">
        <p>{statusMessage}</p>
      </div>
    {:else if status === 'error'}
      <div data-testid="editor-error" role="alert" class="editor-state editor-state--error">
        <p>{statusMessage}</p>
      </div>
    {/if}
  {/if}
</section>

<style>
  .editor-page {
    display: flex;
    flex-direction: column;
    gap: 12px;
    max-width: 900px;
  }
  .editor {
    border: 1px solid var(--sp-border);
    border-radius: var(--sp-radius);
    min-height: 340px;
    outline: none;
  }
  .editor:focus {
    border-color: var(--sp-accent);
  }
  .editor-controls {
    display: flex;
    gap: 8px;
    align-items: center;
  }
  .editor-button {
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
  .editor-button:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }
  .editor-button:focus-visible {
    outline: 3px solid var(--sp-accent);
    outline-offset: 2px;
  }
  .editor-button--outline {
    background: transparent;
    border-color: var(--sp-border-strong);
  }
  .editor-state {
    padding: 10px 14px;
    border-radius: var(--sp-radius-sm);
    font-size: 0.9375rem;
  }
  .editor-state p { margin: 0; }
  .editor-state--conflict {
    border: 1px solid var(--sp-warn);
    background: color-mix(in srgb, var(--sp-warn) 12%, transparent);
    color: var(--sp-fg);
  }
  .editor-state--saved {
    border: 1px solid var(--sp-ok);
    background: color-mix(in srgb, var(--sp-ok) 12%, transparent);
    color: var(--sp-fg);
  }
  .editor-state--error {
    border: 1px solid var(--sp-danger);
    background: color-mix(in srgb, var(--sp-danger) 12%, transparent);
    color: var(--sp-fg);
  }
</style>
