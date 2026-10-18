<script lang="ts">
  /* components/LiveRegion.svelte — the single aria-live announce region (slice S4).
   *
   * Mounted once in App.svelte. Registers itself with lib/announce.ts so any
   * component can call announce(...) to surface async updates to screen
   * readers. Visually hidden (sr-only token) but present in the accessibility
   * tree.
   */
  import { onMount, onDestroy } from 'svelte';
  import { setAnnounceNode } from '../lib/announce.ts';

  let el: HTMLDivElement;

  onMount(() => setAnnounceNode(el));
  onDestroy(() => setAnnounceNode(null));
</script>

<div bind:this={el} aria-live="polite" class="sr-only" data-testid="aria-live"></div>
