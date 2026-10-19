/* web/src/lib/stores/now.svelte.ts — glanceable "now" focus state for the TV
 * NOW banner (Phase 1/G Task 2).
 *
 * Holds the current in-table focus consumed by NowBanner (TV viewport only):
 *   - currentPage   : the note/page currently on screen
 *   - revealPending : whether any GM block on the page is still unrevealed
 *   - lastRoll      : { formula, total } of the most recent dice roll
 *
 * Module-level Svelte 5 runes (same pattern as session.svelte.ts / sse.svelte.ts)
 * give one reactive store any component can read. Writers: WikiReader sets
 * currentPage + revealPending; DiceRoller sets lastRoll. No API calls here.
 */

let currentPage = $state<string | null>(null);
let lastRoll = $state<{ formula: string; total: number } | null>(null);
let revealPending = $state(false);

export const now = {
  /** Note/path currently in focus, or null. */
  get currentPage(): string | null {
    return currentPage;
  },
  /** Most recent roll { formula, total }, or null. */
  get lastRoll(): { formula: string; total: number } | null {
    return lastRoll;
  },
  /** True when at least one GM block on the page is still unrevealed. */
  get revealPending(): boolean {
    return revealPending;
  },

  setCurrentPage(page: string | null) {
    currentPage = page;
  },
  setLastRoll(formula: string, total: number) {
    lastRoll = { formula, total };
  },
  /** Clear the last roll (nothing rolled yet). */
  clearLastRoll() {
    lastRoll = null;
  },
  setRevealPending(pending: boolean) {
    revealPending = pending;
  },
};
