<script lang="ts">
  import { onMount } from 'svelte';

  let health: string = 'checking…';

  onMount(async () => {
    try {
      const res = await fetch('/healthz');
      const body = await res.json();
      health = res.ok && body.status === 'ok' ? 'ok' : `unexpected: ${res.status}`;
    } catch (e) {
      health = `unreachable (${e})`;
    }
  });
</script>

<main class="mx-auto max-w-2xl p-8" style="background: var(--semiplane-bg); color: var(--semiplane-fg)">
  <h1 class="text-2xl font-bold" data-testid="app-title">Semiplane</h1>
  <p class="mt-2" data-testid="health">backend: {health}</p>
  <p class="mt-4 text-sm opacity-70">Thin slice — wiki, editor, dice, and creator land here.</p>
</main>
