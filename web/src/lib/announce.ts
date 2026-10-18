/* web/src/lib/announce.ts — aria-live announcement helper (slice S4).
 *
 * Components call announce(message) to surface async updates (SSE reveals,
 * dice results) to screen readers. Pending states must be text, never
 * shimmer-only (tech-stack §9 / docs/e2e.md Accessibility).
 *
 * A LiveRegion component (mounted once in App.svelte) registers the live
 * region node; announce() writes into it, resetting first so the same
 * message re-announces on repeat.
 */

let node: HTMLElement | null = null;

export function setAnnounceNode(n: HTMLElement | null) {
  node = n;
}

export function announce(message: string) {
  if (!node || !message) return;
  node.textContent = '';
  // Force a reflow so assigning the same text re-triggers the announcement.
  void node.offsetHeight;
  node.textContent = message;
}
