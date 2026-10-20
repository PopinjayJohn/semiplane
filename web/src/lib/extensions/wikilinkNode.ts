/* web/src/lib/extensions/wikilinkNode.ts — TipTap custom node: inline wikilink
 *
 * Markdown syntax: `[[page]]`, `[[page#anchor]]`, `[[page|alias]]`,
 * `[[page#anchor|alias]]` — mirrors internal/markdown/wikilink.go exactly:
 *   - single-line span; `]]` must close on the same line
 *   - empty target or a nested `[[` inside the span stays literal text
 *     (a half-written link must stay visible, never vanish)
 *   - `page|` (empty alias) renders with the whole inner span as label
 *   - label falls back to the full inner text when no alias is given
 *
 * Implements all three markdown hooks (tech-stack §9 / implementation plan §F):
 *   1. markdownTokenizer — recognises `[[…]]` inline spans
 *   2. parseMarkdown       — token → TipTap JSON (markdown → doc)
 *   3. renderMarkdown      — TipTap JSON → markdown string (doc → markdown)
 *
 * Without hook 1 the Markdown lexer escapes the brackets to `\[\[…\]\]`, so
 * every editor save would corrupt the note's wikilinks (data mangling).
 */

import { Node } from '@tiptap/core';

export interface WikilinkAttrs {
  target: string;
  anchor: string;
  label: string;
}

/** Parses the inner span of `[[inner]]` into {target, anchor, label}, or null
 *  when it does not form a wikilink (mirrors wikilinkParser.Parse in Go). */
export function parseWikilinkInner(inner: string): WikilinkAttrs | null {
  const trimmed = inner;
  if (trimmed.length === 0 || trimmed.includes('[[')) return null;
  const [targetPart, display] = trimmed.split('|');
  const [page, anchor = ''] = targetPart.split('#');
  if (page.trim() === '') return null;
  let label = (display ?? '').trim();
  if (label === '') {
    label = inner.trim();
  }
  return { target: page.trim(), anchor: anchor.trim(), label };
}

/** Serialises attrs back to the inner span, omitting empty parts. */
export function renderWikilinkInner(attrs: WikilinkAttrs): string {
  const target = attrs.anchor ? `${attrs.target}#${attrs.anchor}` : attrs.target;
  if (attrs.label && attrs.label !== target) {
    return `[[${target}|${attrs.label}]]`;
  }
  return `[[${target}]]`;
}

export const wikilinkNode = Node.create({
  name: 'wikilink',
  group: 'inline',
  inline: true,
  atom: true,

  addAttributes() {
    return {
      target: {
        default: '',
        parseHTML: (element) => element.getAttribute('data-wikilink') || '',
      },
      anchor: {
        default: '',
        parseHTML: (element) => element.getAttribute('data-wikilink-anchor') || '',
      },
      label: {
        default: '',
        parseHTML: (element) =>
          element.getAttribute('data-wikilink-label') ||
          element.textContent ||
          '',
      },
    };
  },

  parseHTML() {
    return [
      {
        tag: 'span[data-wikilink]',
        getAttrs: (element) => ({
          target: element.getAttribute('data-wikilink') || '',
          anchor: element.getAttribute('data-wikilink-anchor') || '',
          label:
            element.getAttribute('data-wikilink-label') ||
            element.textContent ||
            '',
        }),
      },
    ];
  },

  renderHTML({ node }) {
    const attrs = {
      target: (node.attrs?.target as string) ?? '',
      anchor: (node.attrs?.anchor as string) ?? '',
      label: (node.attrs?.label as string) ?? '',
    };
    // Server renderer emits class="wikilink" anchors for the reader; the
    // editor shows the same look as an inert span (no navigation while
    // editing). data-testid mirrors the reader for the axe scan.
    return [
      'span',
      {
        'data-wikilink': attrs.target,
        'data-wikilink-anchor': attrs.anchor || undefined,
        'data-wikilink-label': attrs.label,
        'data-testid': 'wikilink',
        class: 'wikilink',
      },
      attrs.label || attrs.target,
    ];
  },

  // 1 · tokenizer: recognise `[[inner]]` inline spans (single line only)
  markdownTokenizer: {
    name: 'wikilink',
    level: 'inline',
    start: (src) => src.indexOf('[['),
    tokenize: (src, _tokens, _lexer) => {
      const match = /^\[\[([^\]\n]*?)\]\]/.exec(src);
      if (!match) return undefined;
      const attrs = parseWikilinkInner(match[1]);
      if (!attrs) return undefined; // keep literal text (mirrors Go parser)
      return { type: 'wikilink', raw: match[0], ...attrs };
    },
  },

  // 2 · parse: token → TipTap JSON (leaf node, no child content)
  parseMarkdown: (token) => ({
    type: 'wikilink',
    attrs: { target: token.target, anchor: token.anchor, label: token.label },
  }),

  // 3 · render: TipTap JSON → markdown
  renderMarkdown: (node) => {
    const attrs: WikilinkAttrs = {
      target: (node.attrs?.target as string) ?? '',
      anchor: (node.attrs?.anchor as string) ?? '',
      label: (node.attrs?.label as string) ?? '',
    };
    return renderWikilinkInner(attrs);
  },
});
