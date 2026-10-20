/* web/src/lib/editor-frontmatter.ts — frontmatter isolation for the editor.
 *
 * The @tiptap Markdown extension has no YAML-frontmatter concept, so a
 * document like `---\ntags: [npc]\n---\n\n# Elora` parses the fences as
 * semantic markup (thematic break / after-fence headings) and the YAML line
 * as `## tags: \[npc\]` — every editor save would then corrupt the note's
 * frontmatter. Fix: split the frontmatter off before the editor loads the
 * body, and re-attach it verbatim to editor.getMarkdown() output at save.
 *
 * Mirrors internal/markdown/doc.go ParseFrontmatter's fence recognition so
 * the editor and the server agree on what is frontmatter:
 *   - optional UTF-8 BOM, first line `---` (+ horizontal whitespace)
 *   - closing line is `---` or `...` (+ horizontal whitespace)
 *   - an unclosed opening fence is NOT frontmatter (fail open: e.g. a
 *     document that opens with a thematic break stays literal)
 * The byte-exact front slice is returned, so frontmatter is preserved
 * literally without re-parsing YAML in the browser.
 */

/** Fence detection identical to isFenceLine in Go: exactly the fence plus
 *  horizontal whitespace (line already has its newline stripped). */
function isFenceLine(line: string, fence: string): boolean {
  if (!line.startsWith(fence)) return false;
  for (const c of line.slice(fence.length)) {
    if (c !== ' ' && c !== '\t') return false;
  }
  return true;
}

export interface FrontmatterSplit {
  /** Byte-exact frontmatter slice including fences and the trailing
   *  newline, or '' when the document has no frontmatter. */
  frontmatter: string;
  /** Everything after the frontmatter (whole content when none). */
  body: string;
}

const BOM = '\uFEFF';

export function splitFrontmatter(content: string): FrontmatterSplit {
  let src = content;
  let consumedBom = false;
  if (src.startsWith(BOM)) {
    consumedBom = true;
    src = src.slice(BOM.length);
  }

  // Cut lines from src; `rest` tracks the unconsumed suffix. The front
  // slice is rebuilt from the ORIGINAL string so it is byte-exact.
  let rest = src;
  const openLine = cutLineLocal(rest);
  if (openLine === null) {
    return { frontmatter: '', body: content };
  }
  const [firstLine, restAfterFirst] = openLine;
  if (!isFenceLine(firstLine, '---')) {
    // No frontmatter at all: return the original content untouched.
    return { frontmatter: '', body: content };
  }
  if (restAfterFirst === null) {
    // `---` with no further line: fail open (no closing fence).
    return { frontmatter: '', body: content };
  }
  rest = restAfterFirst;
  while (true) {
    if (rest.length === 0) {
      // No closing fence: not frontmatter — fail open to literal body.
      return { frontmatter: '', body: content };
    }
    const cut = cutLineLocal(rest);
    if (cut === null) {
      // Unterminated (no newline at all): no closing fence → fail open.
      return { frontmatter: '', body: content };
    }
    const [line, next] = cut;
    const afterLine = next === null ? '' : next;
    if (isFenceLine(line, '---') || isFenceLine(line, '...')) {
      // Closing fence found: advance past it, then the front slice is
      // byte-exact from the original start through this fence line and
      // its newline (including the BOM when present).
      const frontLen = content.length - afterLine.length;
      return { frontmatter: content.slice(0, frontLen), body: content.slice(frontLen) };
    }
    rest = afterLine;
    if (next === null) {
      // Unterminated: no closing fence → fail open.
      return { frontmatter: '', body: content };
    }
  }
}

/** Mirrors cutLine in Go: strips \r\n; the final unterminated line yields
 *  null for the remainder. */
function cutLineLocal(
  content: string,
): [string, string | null] | null {
  if (content.length === 0) return null;
  const i = content.indexOf('\n');
  if (i >= 0) {
    return [content.slice(0, i).replace(/\r$/, ''), content.slice(i + 1)];
  }
  return [content.replace(/\r$/, ''), null];
}

/** Reassembles a saved editor body under its original frontmatter. */
export function joinFrontmatter(frontmatter: string, body: string): string {
  return frontmatter + body;
}
