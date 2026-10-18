/* web/src/lib/extensions/statblockNode.ts — TipTap custom node: statblock card
 *
 * Markdown syntax:
 *   :::statblock
 *   <statblock content>
 *   :::
 *
 * Implements all three markdown hooks (tokenizer, parseMarkdown, renderMarkdown).
 */

import { Node } from '@tiptap/core';

export const statblockNode = Node.create({
  name: 'statblock',
  group: 'block',
  content: 'block+',

  addAttributes() {
    return {
      name: {
        default: '',
        parseHTML: (element) => element.getAttribute('data-statblock-name') || '',
      },
    };
  },

  parseHTML() {
    return [
      {
        tag: 'div[data-statblock]',
        getAttrs: (element) => ({
          name: element.getAttribute('data-statblock-name') || '',
        }),
      },
    ];
  },

  renderHTML({ node }) {
    return [
      'div',
      {
        'data-statblock': '',
        'data-statblock-name': node.attrs?.name ?? '',
        class: 'statblock',
      },
      0,
    ];
  },

  // 1 · tokenizer: recognise `:::statblock ... :::` block syntax
  markdownTokenizer: {
    name: 'statblock',
    level: 'block',
    start: (src) => src.indexOf(':::statblock'),
    tokenize: (src, _tokens, lexer) => {
      const match = /^:::statblock\n([\s\S]*?)\n:::/.exec(src);
      if (!match) return undefined;
      return {
        type: 'statblock',
        raw: match[0],
        name: '',
        tokens: lexer.blockTokens(match[1]),
      };
    },
  },

  // 2 · parse: token → Tiptap JSON
  parseMarkdown: (token, helpers) => ({
    type: 'statblock',
    attrs: { name: token.name ?? '' },
    content: helpers.parseBlockChildren?.(token.tokens || []) ?? [],
  }),

  // 3 · render: Tiptap JSON → markdown
  renderMarkdown: (node, helpers) => {
    const content = helpers.renderChildren(node.content || []);
    return `:::statblock\n${content}\n:::\n\n`;
  },
});
