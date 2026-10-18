/* web/src/lib/extensions/index.ts — custom TipTap node extensions (slice S5).
 *
 * Every custom node implements all three markdown hooks (tokenizer +
 * parseMarkdown + renderMarkdown) so markdown content round-trips through
 * editor.getMarkdown() instead of silently dropping.
 */

import { diceNode } from './diceNode.js';
import { gmBlockNode } from './gmBlockNode.js';
import { statblockNode } from './statblockNode.js';

export { diceNode };
export { gmBlockNode };
export { statblockNode };

export const customNodes = [diceNode, gmBlockNode, statblockNode];
