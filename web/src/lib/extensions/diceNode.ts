/* web/src/lib/extensions/diceNode.ts — TipTap custom node: inline dice button
 *
 * Markdown syntax: `dice:2d20kh1+5`
 *
 * Implements all three markdown hooks (tech-stack §9 / implementation plan §F):
 *   1. markdownTokenizer — recognises `dice:<formula>` inline syntax
 *   2. parseMarkdown       — token → Tiptap JSON (markdown → doc)
 *   3. renderMarkdown      — Tiptap JSON → markdown string (doc → markdown)
 *
 * Without all three hooks, content silently drops on editor.getMarkdown().
 */

import { Node } from '@tiptap/core';

export const diceNode = Node.create({
  name: 'diceButton',
  group: 'inline',
  inline: true,

  addAttributes() {
    return {
      formula: {
        default: '2d20kh1+5',
        parseHTML: (element) => element.getAttribute('data-formula') || '',
      },
    };
  },

  parseHTML() {
    return [
      {
        tag: 'span[data-dice-button]',
        getAttrs: (element) => ({
          formula: element.getAttribute('data-formula') || '2d20kh1+5',
        }),
      },
    ];
  },

  renderHTML({ node }) {
    return [
      'span',
      {
        'data-dice-button': '',
        'data-formula': node.attrs?.formula ?? '2d20kh1+5',
        'aria-label': `Roll dice ${node.attrs?.formula ?? '2d20kh1+5'}`,
        role: 'button',
        tabindex: 0,
        class: 'dice-button',
      },
      `🎲 ${node.attrs?.formula ?? '2d20kh1+5'}`,
    ];
  },

  // 1 · tokenizer: recognise `dice:<formula>` inline
  markdownTokenizer: {
    name: 'dice',
    level: 'inline',
    start: (src) => src.indexOf('dice:'),
    tokenize: (src, _tokens, _lexer) => {
      const match = /^dice:([^\s\n\r]+)/.exec(src);
      if (!match) return undefined;
      return {
        type: 'diceButton',
        raw: match[0],
        formula: match[1],
      };
    },
  },

  // 2 · parse: token → Tiptap JSON (leaf node, no child content)
  parseMarkdown: (token) => ({
    type: 'diceButton',
    attrs: { formula: token.formula },
  }),

  // 3 · render: Tiptap JSON → markdown
  renderMarkdown: (node) => `dice:${node.attrs?.formula ?? '2d20kh1+5'}`,
});
