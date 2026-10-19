<script lang="ts">
  /*
   * components/DiceRoller.svelte — dice roller widget with breakdown popover.
   *
   * Wired to POST /api/v1/dice/roll ({formula} → {formula, total, faces, seed}).
   * Default formula: 2d20kh1+5 (advantage d20 roll, the table's most common
   * check; also the diceNode markdown default).
   *
   * Readout source of truth: the POST response. We do NOT subscribe to the SSE
   * `dice_rolled` broadcast here — for a self-roll both the POST response and
   * the broadcast arrive, which would double-announce and double-paint. SSE
   * `dice_rolled` is for other clients' rolls (out of scope this slice).
   *
   * The breakdown popover re-requests the same formula with ?breakdown=1,
   * replaying the last roll's seed so the kept/discarded faces match the
   * displayed total (a fresh unseeded roll only when no result exists yet
   * or the formula changed).
   *
   * Roll results announce into the polite aria-live region (formula + total)
   * via lib/announce.ts — the same precedent F's reveal flow uses.
   *
   * Strict-CSP clean: no inline handlers, no style= attributes, every
   * interactive element carries data-test=.
   */
  import { rollDice } from '../lib/api/dice.js';
  import { announce } from '../lib/announce.ts';
  import { now } from '../lib/stores/now.svelte.ts';

  let formula = $state('2d20kh1+5');
  let result = $state<{
    formula: string;
    total: number;
    faces: number[];
    seed: number;
  } | null>(null);
  let error = $state<string | null>(null);
  let rolling = $state(false);

  let breakdownOpen = $state(false);
  let breakdown = $state<{
    kept: number[];
    discarded: number[];
    breakdown: string[];
  } | null>(null);
  let breakdownError = $state<string | null>(null);
  let breakdownLoading = $state(false);

  async function handleRoll() {
    error = null;
    result = null;
    rolling = true;
    try {
      const r = await rollDice(formula);
      result = r;
      announce(`Rolled ${r.formula} = ${r.total}`);
      now.setLastRoll(r.formula, r.total);
    } catch (err) {
      error = err instanceof Error ? err.message : 'Roll failed';
    } finally {
      rolling = false;
    }
  }

  async function handleBreakdown() {
    if (breakdownOpen) {
      breakdownOpen = false;
      return;
    }
    breakdownOpen = true;
    breakdown = null;
    breakdownError = null;
    breakdownLoading = true;
    try {
      // Replay the last roll deterministically when we can: pass its seed so
      // the server reproduces the exact same faces the user just saw, matching
      // the displayed total. A fresh (unseeded) breakdown only happens when no
      // result exists yet or the formula changed since the last roll.
      const seed = result && result.formula === formula ? result.seed : undefined;
      const r = await rollDice(formula, { breakdown: true, seed });
      breakdown = {
        kept: r.kept ?? [],
        discarded: r.discarded ?? [],
        breakdown: r.breakdown ?? [],
      };
    } catch (err) {
      breakdownError = err instanceof Error ? err.message : 'Breakdown failed';
    } finally {
      breakdownLoading = false;
    }
  }
</script>

<section data-testid="dice-roller" class="dice-roller" aria-label="Dice roller">
  <div class="dice-controls">
    <label for="dice-formula" class="dice-label">Formula</label>
    <input
      id="dice-formula"
      data-testid="dice-formula"
      type="text"
      bind:value={formula}
      placeholder="2d20kh1+5"
      disabled={rolling}
    />
    <button
      data-testid="dice-roll-btn"
      type="button"
      disabled={rolling}
      onclick={handleRoll}
      class="dice-button dice-button--roll"
    >
      {rolling ? 'Rolling…' : 'Roll'}
    </button>
  </div>

  {#if result}
    <div data-testid="dice-result" class="dice-result">
      <span class="dice-formula">{result.formula}</span>
      <span class="dice-total" data-testid="dice-total">{result.total}</span>
    </div>
  {/if}
  {#if error}
    <p data-testid="dice-error" role="alert" class="dice-error">{error}</p>
  {/if}

  <button
    data-testid="dice-breakdown"
    type="button"
    onclick={handleBreakdown}
    class="dice-button dice-button--breakdown"
    aria-expanded={breakdownOpen}
  >
    {breakdownOpen ? 'Hide breakdown' : 'Breakdown'}
  </button>

  {#if breakdownOpen}
    <div
      data-testid="dice-breakdown-panel"
      class="dice-breakdown"
      role="region"
      aria-label="Roll breakdown"
    >
      {#if breakdownLoading}
        <p role="status">Computing breakdown…</p>
      {:else if breakdownError}
        <p role="alert" class="dice-error">{breakdownError}</p>
      {:else if breakdown}
        <div class="dice-breakdown-section">
          <p class="dice-breakdown-title">Kept</p>
          <ul>
            {#each breakdown.kept as face (face)}
              <li class="dice-face">{face}</li>
            {/each}
          </ul>
        </div>
        <div class="dice-breakdown-section">
          <p class="dice-breakdown-title">Discarded</p>
          <ul>
            {#each breakdown.discarded as face (face)}
              <li class="dice-face">{face}</li>
            {/each}
          </ul>
        </div>
        <div class="dice-breakdown-section">
          <p class="dice-breakdown-title">Per-tier contribution</p>
          <ul>
            {#each breakdown.breakdown as line (line)}
              <li class="dice-breakdown-line">{line}</li>
            {/each}
          </ul>
        </div>
      {:else}
        <p>Open the breakdown to see kept/discarded dice and per-tier lines.</p>
      {/if}
    </div>
  {/if}
</section>

<style>
  .dice-roller {
    display: flex;
    flex-direction: column;
    gap: 12px;
    padding: 14px;
    border: 1px solid var(--sp-border);
    border-radius: var(--sp-radius);
    background: var(--sp-bg-raised);
  }
  .dice-controls {
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
  }
  .dice-label {
    font-size: 0.875rem;
    color: var(--sp-fg-muted);
    margin: 0;
  }
  .dice-controls input {
    flex: 1 1 180px;
    min-width: 120px;
    padding: 8px 10px;
    border: 1px solid var(--sp-border);
    border-radius: var(--sp-radius-sm);
    background: var(--sp-bg-raised);
    color: var(--sp-fg);
    font: inherit;
    font-size: 0.9375rem;
    font-family: var(--sp-mono);
  }
  .dice-controls input:focus-visible {
    outline: 3px solid var(--sp-accent);
    outline-offset: 2px;
    border-color: var(--sp-accent);
  }
  .dice-button {
    display: inline-flex;
    align-items: center;
    padding: 8px 14px;
    border: 1px solid var(--sp-border);
    border-radius: var(--sp-radius-sm);
    background: var(--sp-bg-raised);
    color: var(--sp-fg);
    font: inherit;
    font-size: 0.9375rem;
    cursor: pointer;
    min-height: 40px;
  }
  .dice-button:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }
  .dice-button:focus-visible {
    outline: 3px solid var(--sp-accent);
    outline-offset: 2px;
  }
  .dice-button--roll {
    background: var(--sp-accent);
    color: var(--sp-accent-contrast);
    border-color: var(--sp-accent);
    font-weight: 600;
  }
  .dice-button--breakdown {
    background: transparent;
    border-color: var(--sp-border-strong);
  }
  .dice-result {
    display: flex;
    align-items: baseline;
    gap: 8px;
    padding: 10px 14px;
    border-radius: var(--sp-radius-sm);
    background: color-mix(in srgb, var(--sp-accent) 12%, transparent);
    border: 1px solid var(--sp-accent);
    color: var(--sp-fg);
    font-size: 1.125rem;
    font-weight: 700;
  }
  .dice-formula {
    font-family: var(--sp-mono);
    font-size: 0.9375rem;
    color: var(--sp-fg-muted);
  }
  .dice-total {
    font-size: 1.5rem;
  }
  .dice-error {
    margin: 0;
    font-size: 0.875rem;
    color: var(--sp-danger);
  }
  .dice-breakdown {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 10px 14px;
    border: 1px solid var(--sp-border);
    border-radius: var(--sp-radius-sm);
    background: var(--sp-bg-overlay);
  }
  .dice-breakdown-section {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .dice-breakdown-title {
    margin: 0;
    font-size: 0.875rem;
    color: var(--sp-fg-muted);
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .dice-breakdown ul {
    margin: 0;
    padding-left: 18px;
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    list-style: none;
  }
  .dice-face {
    padding: 4px 8px;
    border: 1px solid var(--sp-border);
    border-radius: var(--sp-radius-sm);
    background: var(--sp-bg-raised);
    color: var(--sp-fg);
    font-size: 0.9375rem;
    font-family: var(--sp-mono);
  }
  .dice-breakdown-line {
    margin: 0;
    padding: 4px 8px;
    border-radius: var(--sp-radius-sm);
    background: var(--sp-bg-raised);
    color: var(--sp-fg);
    font-size: 0.9375rem;
    font-family: var(--sp-mono);
  }
  .dice-breakdown p[role='status'],
  .dice-breakdown p[role='alert'] {
    margin: 0;
    font-size: 0.9375rem;
    color: var(--sp-fg-muted);
  }
  .dice-breakdown p[role='alert'] {
    color: var(--sp-danger);
  }
</style>
