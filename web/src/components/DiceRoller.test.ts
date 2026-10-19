import { describe, expect, it, vi, beforeEach } from 'vitest';
import { mount } from 'svelte';
import DiceRoller from './DiceRoller.svelte';

// --- mocks (vi.hoisted so the hoisted vi.mock factories can reference them) --

const dice = vi.hoisted(() => ({ rollDice: vi.fn() }));
vi.mock('../lib/api/dice.js', () => dice);

const alerter = vi.hoisted(() => ({ announce: vi.fn() }));
vi.mock('../lib/announce.ts', () => alerter);

// The mocked bindings are typed as the real functions; widen to the mock
// type so we can stub and assert on them.
const roll = dice.rollDice as ReturnType<typeof vi.fn>;
const speak = alerter.announce as ReturnType<typeof vi.fn>;

function mountRoller(props: Record<string, unknown> = {}) {
  const target = document.createElement('div');
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const instance = mount(DiceRoller as any, { target, props });
  const destroy =
    typeof instance.destroy === 'function' ? instance.destroy : () => {};
  return { target, destroy };
}

function testid(root: Element, id: string) {
  const el = root.querySelector(`[data-testid="${id}"]`);
  if (!el) throw new Error(`no element with data-testid="${id}"`);
  return el as HTMLElement;
}

describe('DiceRoller', () => {
  beforeEach(() => {
    roll.mockReset();
    speak.mockReset();
  });

  it('defaults the formula to 2d20kh1+5 and shows roll button + result readout', () => {
    const { target, destroy } = mountRoller();
    expect(
      (testid(target, 'dice-formula') as HTMLInputElement).value
    ).toBe('2d20kh1+5');
    expect(testid(target, 'dice-roll-btn').textContent).toBe('Roll');
    expect(target.querySelector('[data-testid="dice-result"]')).toBeNull();
    destroy();
  });

  it('rolls via POST, paints formula + total, and announces into aria-live', async () => {
    roll.mockResolvedValue({
      formula: '2d20kh1+5',
      total: 18,
      faces: [20, 3],
      seed: 42,
    });
    const { target, destroy } = mountRoller();
    await testid(target, 'dice-roll-btn').click();
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(roll).toHaveBeenCalledTimes(1);
    expect(roll).toHaveBeenCalledWith('2d20kh1+5');
    const result = testid(target, 'dice-result');
    expect(result.textContent).toContain('2d20kh1+5');
    expect(result.textContent).toContain('18');
    // aria-live announce semantics: polite region told formula + total
    expect(speak).toHaveBeenCalledWith('Rolled 2d20kh1+5 = 18');
    destroy();
  });

  it('shows an error when the roll fails and does not announce', async () => {
    roll.mockRejectedValue(new Error('invalid formula'));
    const { target, destroy } = mountRoller();
    await testid(target, 'dice-roll-btn').click();
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(testid(target, 'dice-error').textContent).toBe('invalid formula');
    expect(target.querySelector('[data-testid="dice-result"]')).toBeNull();
    expect(speak).not.toHaveBeenCalled();
    destroy();
  });

  it('breakdown popover re-rolls with ?breakdown=1 and lists kept/discarded/lines', async () => {
    roll.mockResolvedValue({
      formula: '2d20kh1+5',
      total: 18,
      faces: [20, 3],
      seed: 42,
      kept: [20],
      discarded: [3],
      breakdown: ['dice: 20 (kept, advantage)', 'flat: +5'],
    });
    const { target, destroy } = mountRoller();
    await testid(target, 'dice-roll-btn').click();
    await new Promise((resolve) => setTimeout(resolve, 0));

    // closed -> breakdown hidden
    expect(target.querySelector('[data-testid="dice-breakdown-panel"]')).toBeNull();

    // open -> requests breakdown, replaying the roll's seed so kept/discarded
    // faces reproduce the exact roll the user just saw (same total).
    await testid(target, 'dice-breakdown').click();
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(roll).toHaveBeenLastCalledWith('2d20kh1+5', {
      breakdown: true,
      seed: 42,
    });

    const panel = testid(target, 'dice-breakdown-panel');
    expect(panel.textContent).toContain('Kept');
    expect(panel.textContent).toContain('20');
    expect(panel.textContent).toContain('Discarded');
    expect(panel.textContent).toContain('3');
    expect(panel.textContent).toContain('Per-tier contribution');
    expect(panel.textContent).toContain('dice: 20 (kept, advantage)');
    expect(panel.textContent).toContain('flat: +5');

    // toggle closed again
    await testid(target, 'dice-breakdown').click();
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(target.querySelector('[data-testid="dice-breakdown-panel"]')).toBeNull();
    destroy();
  });

  it('breakdown rolls unseeded when the formula changed since the last roll', async () => {
    roll
      .mockResolvedValueOnce({
        formula: '2d20kh1+5',
        total: 18,
        faces: [20, 3],
        seed: 42,
      })
      .mockResolvedValue({
        formula: '1d6',
        total: 4,
        faces: [4],
        seed: 7,
        kept: [4],
        discarded: [],
        breakdown: ['dice: 4', 'flat: +0'],
      });
    const { target, destroy } = mountRoller();
    await testid(target, 'dice-roll-btn').click();
    await new Promise((resolve) => setTimeout(resolve, 0));

    // user edits the formula, then asks for a breakdown: no seed can replay
    // a roll of the old formula, so the breakdown is a fresh (unseeded) roll.
    const input = testid(target, 'dice-formula') as HTMLInputElement;
    input.value = '1d6';
    input.dispatchEvent(new window.Event('input', { bubbles: true }));
    await testid(target, 'dice-breakdown').click();
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(roll).toHaveBeenLastCalledWith('1d6', { breakdown: true });
    destroy();
  });

  it('displays breakdown error when the breakdown roll fails', async () => {
    roll
      .mockResolvedValueOnce({
        formula: '2d20kh1+5',
        total: 18,
        faces: [20, 3],
        seed: 42,
      })
      .mockRejectedValueOnce(new Error('breakdown boom'));
    const { target, destroy } = mountRoller();
    await testid(target, 'dice-roll-btn').click();
    await testid(target, 'dice-breakdown').click();
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(testid(target, 'dice-breakdown-panel').textContent).toContain(
      'breakdown boom',
    );
    destroy();
  });
});
