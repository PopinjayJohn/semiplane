import { describe, expect, it, afterEach } from 'vitest';
import { Editor, type JSONContent } from '@tiptap/core';
import StarterKit from '@tiptap/starter-kit';
import { Markdown } from '@tiptap/markdown';
import { customNodes } from './index.js';

let editor: Editor | null = null;

function makeEditor() {
  editor = new Editor({
    extensions: [StarterKit, Markdown, ...customNodes],
  });
  return editor;
}

afterEach(() => {
  editor?.destroy();
  editor = null;
});

/** One markdown → doc → markdown round-trip through a real TipTap Editor. */
function roundTrip(markdown: string): string {
  const ed = makeEditor();
  ed.commands.setContent(markdown, { contentType: 'markdown' });
  return ed.getMarkdown();
}

/** Assert the given markers survive a round-trip, and that re-parsing the
 *  round-tripped output still keeps them (content never silently drops on
 *  getMarkdown()). Trailing whitespace may normalize between passes, so we
 *  compare by containment, not exact string equality. */
function survivesRoundTrip(markdown: string, markers: string[]) {
  const out = roundTrip(markdown);
  for (const marker of markers) expect(out).toContain(marker);
  const again = roundTrip(out);
  for (const marker of markers) expect(again).toContain(marker);
}

describe('custom TipTap nodes — markdown round-trip (slice S5)', () => {
  it('dice button round-trips inline', () => {
    survivesRoundTrip('Roll some dice:2d20kh1+5 here.', ['dice:2d20kh1+5']);
  });

  it('dice button standalone round-trips', () => {
    survivesRoundTrip('dice:1d20+3', ['dice:1d20+3']);
  });

  it('gm block round-trips', () => {
    survivesRoundTrip(
      ':::gm\nThe warden is a bone devil.\n:::',
      [':::gm', 'The warden is a bone devil.', ':::'],
    );
  });

  it('gm block preserves inline markdown inside', () => {
    survivesRoundTrip(
      ':::gm\nThe **warden** is a bone devil (Deception +7).\n:::',
      [':::gm', '**warden**', ':::'],
    );
  });

  it('statblock round-trips', () => {
    survivesRoundTrip(
      ':::statblock\nAC 15 · HP 45\n:::',
      [':::statblock', 'AC 15 · HP 45', ':::'],
    );
  });

  it('combined document with all three node types round-trips', () => {
    const src = [
      '# Campaign Notes',
      '',
      'The [[Gloam Gate]] hums beneath the chapel.',
      '',
      'dice:2d20kh1+5',
      '',
      ':::gm',
      'The warden is a disguised bone devil.',
      ':::',
      '',
      ':::statblock',
      'AC 15 · HP 45 · Speed 30',
      ':::',
      '',
    ].join('\n');

    survivesRoundTrip(src, [
      '# Campaign Notes',
      'dice:2d20kh1+5',
      ':::gm',
      'disguised bone devil',
      ':::statblock',
      'AC 15 · HP 45 · Speed 30',
    ]);
  });

  it('editor exposes all three custom node types in the doc', () => {
    const ed = makeEditor();
    ed.commands.setContent(
      'dice:2d20kh1+5\n\n:::gm\nsecret\n:::\n\n:::statblock\nstats\n:::',
      { contentType: 'markdown' },
    );
    const json = ed.getJSON();
    const allTypes = (nodes: JSONContent[]): string[] => {
      let out: string[] = [];
      for (const n of nodes) {
        out.push(String(n.type));
        if (n.content) out.push(...allTypes(n.content));
      }
      return out;
    };
    const types = allTypes(json.content);
    expect(types).toContain('diceButton');
    expect(types).toContain('gmBlock');
    expect(types).toContain('statblock');
    ed.destroy();
  });
});
