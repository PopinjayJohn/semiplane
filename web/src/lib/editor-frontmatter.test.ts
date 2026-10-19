/* editor-frontmatter tests — fence recognition must agree byte-for-byte with
 * internal/markdown/doc.go ParseFrontmatter (the server is source of truth;
 * the editor just isolates the front slice so TipTap never sees it).
 * The expected values below mirror the Go unit tests' conventions. */
import { describe, expect, it } from 'vitest';
import { splitFrontmatter, joinFrontmatter } from './editor-frontmatter.js';

describe('splitFrontmatter', () => {
  it('splits standard frontmatter, body excludes the closing fence', () => {
    const src = '---\ntags: [npc]\n---\n\n# Elora\n\nBody.\n';
    const { frontmatter, body } = splitFrontmatter(src);
    expect(frontmatter).toBe('---\ntags: [npc]\n---\n');
    expect(body).toBe('\n# Elora\n\nBody.\n');
  });

  it('no frontmatter: content returned untouched', () => {
    const src = '# Round\n\nNo frontmatter here.\n';
    const { frontmatter, body } = splitFrontmatter(src);
    expect(frontmatter).toBe('');
    expect(body).toBe(src);
  });

  it('thematic break without closing fence stays literal', () => {
    const src = '---\n# not frontmatter\n';
    const { frontmatter, body } = splitFrontmatter(src);
    expect(frontmatter).toBe('');
    expect(body).toBe(src);
  });

  it('`...` closes the YAML block (yamlEnd)', () => {
    const src = '---\nname: Throg\n...\n\n# Throg\n';
    const { frontmatter, body } = splitFrontmatter(src);
    expect(frontmatter).toBe('---\nname: Throg\n...\n');
    expect(body).toBe('\n# Throg\n');
  });

  it('unclosed opening fence (EOF) fails open to literal body', () => {
    const src = '---\ntags: [npc]\n';
    const { frontmatter, body } = splitFrontmatter(src);
    expect(frontmatter).toBe('');
    expect(body).toBe(src);
  });

  it('`--- ` with trailing horizontal whitespace is still a fence', () => {
    const src = '--- \ntags: npc\n---\t\n\n# R\n';
    const { frontmatter, body } = splitFrontmatter(src);
    expect(frontmatter).toBe('--- \ntags: npc\n---\t\n');
    expect(body).toBe('\n# R\n');
  });

  it('CRLF content keeps its bytes', () => {
    const src = '---\r\ntags: npc\r\n---\r\n\r\n# CRLF\r\n';
    const { frontmatter, body } = splitFrontmatter(src);
    expect(frontmatter).toBe('---\r\ntags: npc\r\n---\r\n');
    expect(body).toBe('\r\n# CRLF\r\n');
  });

  it('a `--- suffix` line is YAML text, not a fence (Go parity)', () => {
    // isFenceLine only matches the exact fence (plus horizontal
    // whitespace); `--- name` stays inside the YAML block and the unterminated
    // document therefore fails open, exactly like the Go parser.
    const src = '---\nname: a\n--- name\n\n# T\n';
    const { frontmatter, body } = splitFrontmatter(src);
    expect(frontmatter).toBe('');
    expect(body).toBe(src);
  });

  it('BOM before the fence is preserved and skipped', () => {
    const src = '﻿---\ntags: npc\n---\n\n# B\n';
    const { frontmatter, body } = splitFrontmatter(src);
    expect(frontmatter.startsWith('﻿')).toBe(true);
    expect(frontmatter).toBe('﻿---\ntags: npc\n---\n');
    expect(body).toBe('\n# B\n');
  });

  it('frontmatter with no YAML body line closes immediately', () => {
    const src = '---\n---\n\n# Empty front\n';
    const { frontmatter, body } = splitFrontmatter(src);
    expect(frontmatter).toBe('---\n---\n');
    expect(body).toBe('\n# Empty front\n');
  });
});

describe('joinFrontmatter', () => {
  it('reassembles the original document byte-exactly', () => {
    const src = '---\ntags: [npc]\n---\n\n# Elora\n\nBody.\n';
    const { frontmatter, body } = splitFrontmatter(src);
    expect(joinFrontmatter(frontmatter, body)).toBe(src);
  });

  it('no frontmatter: body passes through', () => {
    expect(joinFrontmatter('', '# Round\n')).toBe('# Round\n');
  });
});
