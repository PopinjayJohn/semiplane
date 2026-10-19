<script lang="ts">
  /*
   * components/NowBanner.svelte — glanceable NOW banner for the TV viewport.
   *
   * Task 2 interpretation (documented in full in the PR body): a persistent
   * TV-strip banner, rendered only when [data-viewport='tv'], showing the
   * current in-table focus:
   *   - current note/page
   *   - pending-or-done reveal state
   *   - last dice total
   *
   * Reads from the shared `now` rune store (no API calls). Writers:
   * WikiReader (current page + reveal state), DiceRoller (last roll).
   * Shell mounts it only in TV viewport; outside TV it is never rendered.
   */
  import { now } from '../lib/stores/now.svelte.ts';

  let page = $derived(now.currentPage);
  let roll = $derived(now.lastRoll);
  let pending = $derived(now.revealPending);
</script>

<section
  data-testid="now-banner"
  class="now-banner"
  aria-label="Table focus now"
>
  <div class="now-field">
    <span class="now-label">Page</span>
    <span data-testid="now-page" class="now-value">{page ?? '—'}</span>
  </div>
  <div class="now-field">
    <span class="now-label">Reveal</span>
    <span data-testid="now-reveal" class="now-value">{pending ? 'pending' : 'done'}</span>
  </div>
  <div class="now-field">
    <span class="now-label">Last roll</span>
    <span data-testid="now-roll" class="now-value">
      {roll ? `${roll.formula} = ${roll.total}` : '—'}
    </span>
  </div>
</section>

<style>
  .now-banner {
    display: flex;
    gap: 16px;
    align-items: center;
    padding: 8px 16px;
    border-top: 1px solid var(--sp-border);
    background: var(--sp-bg-raised);
    color: var(--sp-fg);
    font-size: 0.9375rem;
  }
  /* TV strip: fixed to the bottom edge (Shell pads .shell-main above it).
   * [data-viewport='tv'] sits on the shell root, outside this component —
   * hence the :global ancestor with a scoped .now-banner descendant. */
  :global([data-viewport='tv']) .now-banner {
    position: fixed;
    bottom: 0;
    left: 0;
    right: 0;
    z-index: 50;
  }
  .now-field {
    display: flex;
    align-items: baseline;
    gap: 6px;
    white-space: nowrap;
  }
  .now-label {
    color: var(--sp-fg-muted);
    font-size: 0.75rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .now-value {
    font-weight: 700;
    font-family: var(--sp-mono);
  }
</style>
