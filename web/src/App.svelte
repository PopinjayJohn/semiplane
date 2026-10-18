<script lang="ts">
  /* App.svelte — root router + auth guard (slice S2).
   *
   * Client-side routing over the history API. Unauthenticated users are
   * redirected to /login; authenticated app routes render Shell.svelte with
   * the route content in its children snippet. Role from the session store
   * is carried into role-scoped UI (GmBlock gating lands in S3).
   *
   * Strict-CSP clean: no inline handlers (onclick/onsubmit bind script
   * functions), no style= attributes, every interactive element has
   * data-test=.
   */
  import { onMount } from 'svelte';
  import { session } from './lib/stores/session.svelte.ts';
  import Shell from './lib/Shell.svelte';
  import Login from './routes/Login.svelte';

  let pathname = $state(window.location.pathname);

  $effect(() => {
    const onPop = () => {
      pathname = window.location.pathname;
    };
    window.addEventListener('popstate', onPop);
    return () => window.removeEventListener('popstate', onPop);
  });

  onMount(async () => {
    await session.init();
  });

  /* Auth guard: once the session has settled, unauthenticated traffic never
   * renders an app route — replace the URL with /login so the back button
   * can't return to a rejected page. */
  $effect(() => {
    if (!session.loaded) return;
    if (!session.isAuthenticated && pathname !== '/login') {
      window.location.replace('/login');
      pathname = '/login';
    }
  });

  function isAppRoute() {
    return pathname === '/' || pathname.startsWith('/wiki/');
  }

  function go(to: string) {
    window.history.pushState(null, '', to);
    pathname = to;
  }
</script>

<main data-testid="app-root" class="app">
  {#if session.loaded && session.isAuthenticated && isAppRoute()}
    <Shell>
      <div data-testid="app-body" class="app-body">
        {#if pathname === '/'}
          <div class="app-welcome">
            <h2 data-testid="app-welcome-title" class="app-welcome-title">Welcome, {session.role}.</h2>
            <p data-testid="app-welcome-body" class="app-welcome-body">
              Choose a note from the sidebar or open the wiki to read.
            </p>
            <button
              data-testid="app-open-wiki"
              onclick={() => go('/wiki/welcome')}
              class="app-button"
            >
              Open wiki
            </button>
          </div>
        {:else}
          <!-- WikiReader renders here (slice S3). Placeholder until then. -->
          <div data-testid="wiki-placeholder" class="wiki-placeholder">
            <p data-testid="wiki-placeholder-text">
              WikiReader for <strong>{pathname.slice('/wiki/'.length) || '(root)'}</strong>
              lands in slice S3.
            </p>
          </div>
        {/if}

        <div class="app-meta">
          <p data-testid="app-role" class="app-role">Role: <span data-testid="app-role-value">{session.role}</span></p>
          <button
            data-testid="app-logout"
            onclick={() => {
              session.logout();
              go('/login');
            }}
            class="app-button app-button--logout"
          >
            Sign out
          </button>
        </div>
      </div>
    </Shell>
  {:else}
    <Login />
  {/if}
</main>

<style>
  .app {
    display: flex;
    flex-direction: column;
    height: 100vh;
  }
  .app-body {
    padding: 24px;
    display: flex;
    flex-direction: column;
    gap: 20px;
    overflow: auto;
  }
  .app-welcome {
    display: flex;
    flex-direction: column;
    gap: 10px;
    max-width: 420px;
  }
  .app-welcome-title {
    margin: 0;
    font-size: 1.5rem;
    font-weight: 700;
    color: var(--sp-fg);
  }
  .app-welcome-body {
    margin: 0;
    color: var(--sp-fg-muted);
  }
  .app-button {
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
  .app-button:focus-visible {
    outline: 3px solid var(--sp-accent);
    outline-offset: 2px;
  }
  .app-button--logout {
    background: transparent;
    border-color: var(--sp-border-strong);
    margin-left: auto;
  }
  .app-meta {
    display: flex;
    flex-direction: column;
    gap: 10px;
    max-width: 420px;
  }
  .app-role {
    margin: 0;
    font-size: 0.875rem;
    color: var(--sp-fg-muted);
  }
  .wiki-placeholder {
    display: flex;
    flex-direction: column;
    gap: 8px;
    max-width: 420px;
    color: var(--sp-fg-muted);
  }
</style>
