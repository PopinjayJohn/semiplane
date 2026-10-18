<script lang="ts">
  /*
   * Shell.svelte — presentational app shell (slice S1).
   * Svelte 5 runes only ($state / $derived / $effect). Zero API calls.
   * Every color is a --sp-* token from tokens.css; no style= attributes,
   * no inline event handlers (onclick attributes bind script functions).
   */

  let role = $state<'gm' | 'player'>('gm');
  let drawerOpen = $state(false);
  let viewport = $state<'desktop' | 'mobile' | 'tv'>('desktop');

  let { sidebar, children } = $props<{
    sidebar?: () => import('svelte').Snippet;
    children?: () => import('svelte').Snippet;
  }>();

  $effect(() => {
    const root = document.documentElement;
    const update = () => {
      const override = root.dataset.viewport;
      viewport =
        override === 'mobile' || override === 'desktop' || override === 'tv'
          ? (override as 'desktop' | 'mobile' | 'tv')
          : window.innerWidth < 768
            ? 'mobile'
            : 'desktop';
      root.dataset.viewport = viewport;
    };
    update();
    const mq = window.matchMedia('(max-width: 767px)');
    mq.addEventListener('change', update);
    return () => mq.removeEventListener('change', update);
  });

  $effect(() => {
    if (viewport !== 'mobile' || !drawerOpen) return;
    const onEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') drawerOpen = false;
    };
    window.addEventListener('keydown', onEscape);
    return () => window.removeEventListener('keydown', onEscape);
  });

  function toggleRole() {
    role = role === 'gm' ? 'player' : 'gm';
  }

  function toggleDrawer() {
    drawerOpen = !drawerOpen;
  }

  function closeDrawer() {
    drawerOpen = false;
  }
</script>

<div class="shell" data-viewport={viewport} data-test="shell-root">
  <header class="shell-header" data-test="shell-header">
    <button
      class="shell-hamburger"
      data-test="shell-drawer-toggle"
      aria-expanded={drawerOpen}
      aria-controls="shell-drawer"
      aria-label="Toggle navigation"
      onclick={toggleDrawer}
    >
      <span class="sr-only">Toggle navigation</span>
      <span aria-hidden="true">≡</span>
    </button>

    <h1 class="shell-title" data-test="shell-title">Semiplane</h1>

    <button
      class="shell-role-chip"
      class:chip-gm={role === 'gm'}
      class:chip-player={role === 'player'}
      data-test="shell-role-chip"
      onclick={toggleRole}
    >
      {role === 'gm' ? 'GM' : 'Player'}
      <span class="sr-only">role: {role}</span>
    </button>

    <nav class="shell-nav" data-test="shell-nav">
      <button class="shell-nav-item" data-test="shell-nav-notes" onclick={() => {}}>
        Notes
      </button>
      <button class="shell-nav-item" data-test="shell-nav-characters" onclick={() => {}}>
        Characters
      </button>
      <button class="shell-nav-item" data-test="shell-nav-rules" onclick={() => {}}>
        Rules
      </button>
    </nav>
  </header>

  <aside class="shell-sidebar" data-test="shell-sidebar">
    {#if viewport !== 'mobile'}
      {@render sidebar?.()}
    {/if}
  </aside>

  <main class="shell-main" data-test="shell-main">
    {@render children?.()}
  </main>

  {#if viewport === 'mobile'}
    <div
      class="shell-drawer-backdrop"
      data-test="shell-drawer-backdrop"
      aria-hidden="true"
      hidden={!drawerOpen}
    ></div>
    <aside
      class="shell-drawer"
      data-test="shell-drawer"
      id="shell-drawer"
      aria-label="Navigation"
      hidden={!drawerOpen}
    >
      <button
        class="shell-drawer-close"
        data-test="shell-drawer-close"
        aria-label="Close navigation"
        onclick={closeDrawer}
      >
        <span class="sr-only">Close navigation</span>
        <span aria-hidden="true">✕</span>
      </button>
      {@render sidebar?.()}
    </aside>
  {/if}
</div>

<style>
  /* Layout and focus only — every color comes from --sp-* tokens. */

  .shell {
    display: flex;
    flex-direction: column;
    height: 100vh;
    min-height: 0;
  }

  .shell-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 0 16px;
    position: sticky;
    top: 0;
    z-index: 20;
    border-bottom: 1px solid var(--sp-border);
    background: var(--sp-bg-raised);
  }

  .shell-hamburger {
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 8px;
    border: none;
    border-radius: var(--sp-radius-sm);
    background: transparent;
    color: var(--sp-fg);
    cursor: pointer;
  }

  .shell-title {
    margin: 0;
    font-size: 1.25rem;
    font-weight: 700;
    color: var(--sp-fg);
  }

  .shell-role-chip {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 2px 10px;
    border: 1px solid var(--sp-border-strong);
    border-radius: var(--sp-radius);
    background: var(--sp-bg-overlay);
    color: var(--sp-fg);
    font-size: 0.875rem;
    cursor: pointer;
  }

  .shell-role-chip.chip-gm {
    color: var(--sp-gm);
  }

  .shell-role-chip.chip-player {
    color: var(--sp-player);
  }

  .shell-nav {
    display: flex;
    gap: 8px;
    padding: 0 12px;
  }

  .shell-nav-item {
    padding: 6px 12px;
    border: none;
    border-radius: var(--sp-radius-sm);
    background: transparent;
    color: var(--sp-fg-muted);
    cursor: pointer;
  }

  .shell-sidebar {
    flex: 0 0 260px;
    border-right: 1px solid var(--sp-border);
    background: var(--sp-bg-overlay);
    display: flex;
    flex-direction: column;
    min-width: 0;
  }

  .shell-main {
    flex: 1 1 auto;
    overflow: auto;
    padding: 24px;
    min-width: 0;
  }

  .shell-drawer-backdrop {
    position: fixed;
    inset: 0;
    background: var(--sp-bg-overlay);
    z-index: 30;
  }

  .shell-drawer {
    position: fixed;
    top: 0;
    left: 0;
    width: 260px;
    height: 100vh;
    border-right: 1px solid var(--sp-border);
    background: var(--sp-bg-raised);
    z-index: 40;
    display: flex;
    flex-direction: column;
  }

  .shell-drawer-close {
    padding: 10px 16px;
    border: none;
    border-radius: 0;
    background: transparent;
    color: var(--sp-fg);
    font-size: 1.25rem;
    line-height: 1;
    cursor: pointer;
    text-align: left;
  }

  button:focus-visible {
    outline: 3px solid var(--sp-accent);
    outline-offset: 2px;
  }

  /* Desktop: hide the hamburger, show the split sidebar layout. */
  @media (min-width: 768px) {
    .shell-hamburger {
      display: none;
    }
  }

  /* Mobile: the static sidebar is hidden; its slot renders in the drawer. */
  @media (max-width: 767px) {
    .shell-sidebar {
      display: none;
    }
  }
</style>
