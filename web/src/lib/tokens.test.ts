import { describe, expect, it } from 'vitest';

async function readFile(path: string): Promise<string> {
  // Dynamic import keeps the module graph free of hard node types
  // (tsconfig ships no @types/node); vitest runs in the node environment.
  const fs = await import('node:fs' as string);
  return (fs as { readFileSync: (p: string, enc: string) => string }).readFileSync(
    path,
    'utf8'
  );
}

const css = await readFile(new URL('./tokens.css', import.meta.url).pathname);

describe('design tokens (tokens.css)', () => {
  it('declares every surface, border, text, accent, role, dice, slot and shape token', () => {
    for (const token of [
      '--sp-bg',
      '--sp-bg-raised',
      '--sp-bg-overlay',
      '--sp-border',
      '--sp-border-strong',
      '--sp-fg',
      '--sp-fg-muted',
      '--sp-fg-faint',
      '--sp-accent',
      '--sp-accent-contrast',
      '--sp-danger',
      '--sp-warn',
      '--sp-ok',
      '--sp-gm',
      '--sp-player',
      '--sp-crit',
      '--sp-fumble',
      '--sp-secret-border',
      '--sp-slot-toolbar',
      '--sp-slot-sidebar',
      '--sp-slot-panel',
      '--sp-slot-footer',
      '--sp-slot-tray',
      '--sp-radius',
      '--sp-radius-sm',
      '--sp-font',
      '--sp-mono'
    ]) {
      expect(css).toContain(token);
    }
  });

  it('records a contrast ratio vs --sp-bg beside every color token', () => {
    const colorTokens = [
      '--sp-bg',
      '--sp-bg-raised',
      '--sp-bg-overlay',
      '--sp-border',
      '--sp-border-strong',
      '--sp-fg',
      '--sp-fg-muted',
      '--sp-fg-faint',
      '--sp-accent',
      '--sp-accent-contrast',
      '--sp-danger',
      '--sp-warn',
      '--sp-ok',
      '--sp-gm',
      '--sp-player',
      '--sp-crit',
      '--sp-fumble',
      '--sp-secret-border',
      '--sp-slot-toolbar',
      '--sp-slot-sidebar',
      '--sp-slot-panel',
      '--sp-slot-footer',
      '--sp-slot-tray'
    ];
    for (const token of colorTokens) {
      expect(css).toContain(`${token}:`);
      const decl = css.indexOf(`${token}:`);
      const snippet = css.slice(Math.max(0, decl - 400), css.indexOf(';', decl) + 200);
      expect(snippet).toMatch(/contrast/);
      expect(snippet).toMatch(/vs --sp-bg/);
    }
  });

  it('provides a parchment light skin overriding the same tokens', () => {
    expect(css).toContain("[data-theme='parchment']");
    expect(css).toContain('--sp-bg: #f5f0e6');
  });

  it('documents body defaults, reduced-motion guard and TV 64px touch targets', () => {
    expect(css).toContain('background: var(--sp-bg)');
    expect(css).toContain('color: var(--sp-fg)');
    expect(css).toContain('font-family: var(--sp-font)');
    expect(css).toContain('prefers-reduced-motion');
    expect(css).toContain("[data-viewport='tv']");
    expect(css).toContain('min-height: 64px');
    expect(css).toContain('min-width: 64px');
  });
});
