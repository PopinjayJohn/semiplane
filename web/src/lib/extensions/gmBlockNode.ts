/* web/src/lib/extensions/gmBlockNode.ts — TipTap custom node: block-level GM secret
 *
 * Markdown syntax:
 *   :::gm
 *   <secret content>
 *   :::
 *
 * Implements all three markdown hooks (tokenizer, parseMarkdown, renderMarkdown).
 * The role-based stripping of :::gm content happens server-side (WikiReader);
 * this node is the editable authoring surface for GM secrets.
 */

import { Node } from '@tiptap/core';

export const gmBlockNode = Node.create({
  name: 'gmBlock',
  group: 'block',
  content: 'block+',

  addAttributes() {
    return {
      label: {
        default: 'GM',
        parseHTML: (element) => element.getAttribute('data-gm-label') || 'GM',
      },
    };
  },

  parseHTML() {
    return [
      {
        tag: 'div[data-gm-block]',
        getAttrs: (element) => ({
          label: element.getAttribute('data-gm-label') || 'GM',
        }),
      },
    ];
  },

  renderHTML({ node }) {
    return [
      'div',
      {
        'data-gm-block': '',
        'data-gm-label': node.attrs?.label ?? 'GM',
        class: 'gm-block',
      },
      0,
    ];
  },

  // 1 · tokenizer: recognise `:::gm ... :::` block syntax
  markdownTokenizer: {
    name: 'gmBlock',
    level: 'block',
    start: (src) => src.indexOf(':::gm'),
    tokenize: (src, _tokens, lexer) => {
      const match = /^:::gm\n([\s\S]*?)\n:::/.exec(src);
      if (!match) return undefined;
      return {
        type: 'gmBlock',
        raw: match[0],
        label: 'GM',
        tokens: lexer.blockTokens(match[1]),
      };
    },
  },

  // 2 · parse: token → Tiptap JSON
  parseMarkdown: (token, helpers) => ({
    type: 'gmBlock',
    attrs: { label: token.label ?? 'GM' },
    content: helpers.parseBlockChildren?.(token.tokens || []) ?? [],
  }),

  // 3 · render: Tiptap JSON → markdown
  renderMarkdown: (node, helpers) => {
    const content = helpers.renderChildren(node.content || []);
    return `:::gm\n${content}\n:::\n\n`;
  },
});
