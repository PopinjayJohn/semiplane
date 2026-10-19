import { describe, expect, it, beforeEach } from 'vitest';
import { mount } from 'svelte';
import { now } from '../lib/stores/now.svelte.ts';
import NowBanner from './NowBanner.svelte';

function mountBanner() {
  const target = document.createElement('div');
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const instance = mount(NowBanner as any, { target });
  const destroy =
    typeof instance.destroy === 'function' ? instance.destroy : () => {};
  return { target, destroy };
}

function testid(root: Element, id: string) {
  const el = root.querySelector(`[data-testid="${id}"]`);
  if (!el) throw new Error(`no element with data-testid="${id}"`);
  return el as HTMLElement;
}

describe('NowBanner', () => {
  beforeEach(() => {
    now.setCurrentPage(null);
    now.setRevealPending(false);
    now.clearLastRoll();
  });

  it('renders current page, reveal state, and last dice total from the store', () => {
    now.setCurrentPage('campaign/intro.md');
    now.setRevealPending(true);
    now.setLastRoll('2d20kh1+5', 18);

    const { target, destroy } = mountBanner();
    expect(testid(target, 'now-page').textContent).toBe('campaign/intro.md');
    expect(testid(target, 'now-reveal').textContent).toBe('pending');
    expect(testid(target, 'now-roll').textContent).toBe('2d20kh1+5 = 18');
    destroy();
  });

  it('renders done and em-dash when nothing has rolled', () => {
    const { target, destroy } = mountBanner();
    expect(testid(target, 'now-page').textContent).toBe('—');
    expect(testid(target, 'now-reveal').textContent).toBe('done');
    expect(testid(target, 'now-roll').textContent).toBe('—');
    destroy();
  });

  it('reacts to store updates (page change flips the banner)', async () => {
    now.setCurrentPage('campaign/intro.md');
    now.setLastRoll('1d20+3', 7);

    const { target, destroy } = mountBanner();
    expect(testid(target, 'now-page').textContent).toBe('campaign/intro.md');

    now.setCurrentPage('rules/combat.md');
    // Svelte 5 derived DOM commits after the current microtask.
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(testid(target, 'now-page').textContent).toBe('rules/combat.md');
    destroy();
  });
});
