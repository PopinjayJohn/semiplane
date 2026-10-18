<script lang="ts">
  /* App.svelte — root router + auth guard (slice S2).
   *
   * Client-side routing over the history API.
   *   - "/"          : public landing page (app title + health status)
   *   - "/login"     : password login
   *   - "/wiki/:path": authenticated — WikiReader (role-aware, GmBlock gating in S3)
   *   - "/edit/:path": authenticated — TipTap editor (code-split chunk, S5)
   *
   * Only /wiki/* and /edit/* require a session; "/" and "/login" are public.
   * Unauthenticated traffic to a protected route is replaced with /login so the
   * back button can't return to a rejected page.
   *
   * Strict-CSP clean: no inline handlers (onclick/onsubmit bind script
   * functions), no style= attributes, every interactive element has data-test=.
   */
  import { onMount } from 'svelte';
  import { session } from './lib/stores/session.svelte.ts';
  import Shell from './lib/Shell.svelte';
  import Login from './routes/Login.svelte';
  import LiveRegion from './components/LiveRegion.svelte';
  import WikiReader from './routes/WikiReader.svelte';

  let pathname = $state(window.location.pathname);
  let EditorComponent = $state<any>(null);
  let health = $state('checking…');

  $effect(() => {
    const onPop = () => {
      pathname = window.location.pathname;
    };
    window.addEventListener('popstate', onPop);
    return () => window.removeEventListener('popstate', onPop);
  });

  onMount(async () => {
    await session.init();
    try {
      const res = await fetch('/healthz');
      const body = await res.json();
      health = res.ok && body.status === 'ok' ? 'ok' : `unexpected: ${res.status}`;
    } catch (e) {
      health = `unreachable (${e})`;
    }
  });

  /* Auth guard: protect the wiki and editor routes only; the landing page and
   * login stay public. */
  $effect(() => {
    if (!session.loaded) return;
    const protectedRoute =
      pathname.startsWith('/wiki/') || pathname.startsWith('/edit/');
    if (!session.isAuthenticated && protectedRoute && pathname !== '/login') {
      window.location.replace('/login');
      pathname = '/login';
    }
  });

  $effect(() => {
    if (pathname.startsWith('/edit/') && !EditorComponent) {
      import('./routes/Editor.svelte').then((mod) => {
        EditorComponent = mod.default;
      });
    }
  });

  function isAuthRoute() {
    return pathname.startsWith('/wiki/') || pathname.startsWith('/edit/');
  }

  function go(to: string) {
    window.history.pushState(null, '', to);
    pathname = to;
  }
</script>

<main data-testid="app-root" class="app">
  <LiveRegion />
  {#if pathname === '/'}
    <div data-testid="app-welcome-root" class="app-welcome">
      <h1 data-testid="app-title" class="app-title">Semiplane</h1>
      <p data-testid="health" class="app-health">backend: {health}</p>
      <div class="app-welcome-actions">
        <button
          data-testid="app-open-wiki"
          onclick={() => go('/wiki/welcome')}
          class="app-button"
        >
          Open wiki
        </button>
        <button
          data-testid="app-open-editor"
          onclick={() => go('/edit/welcome')}
          class="app-button"
        >
          Open editor
        </button>
        <button
          data-testid="app-open-login"
          onclick={() => go('/login')}
          class="app-button"
        >
          Sign in
        </button>
      </div>
    </div>
  {:else if session.loaded && session.isAuthenticated}
    <Shell>
      <div data-testid="app-body" class="app-body">
        {#if pathname.startsWith('/wiki/')}
          <WikiReader path={pathname.slice('/wiki/'.length)} />
        {:else if pathname.startsWith('/edit/') && EditorComponent}
          <EditorComponent path={pathname.slice('/edit/'.length)} />
        {:else}
          <div data-testid="app-notfound">Not found</div>
        {/if}

        <div class="app-meta">
          <p data-testid="app-role" class="app-role">
            Role: <span data-testid="app-role-value">{session.role}</span>
          </p>
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
  .app-welcome {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    height: 100vh;
    padding: 16px;
    text-align: center;
  }
  .app-title {
    margin: 0 0 12px 0;
    font-size: 2rem;
    font-weight: 700;
    color: var(--sp-fg);
  }
  .app-health {
    margin: 0 0 24px 0;
    font-size: 1rem;
    color: var(--sp-fg-muted);
    font-family: var(--sp-mono);
  }
  .app-welcome-actions {
    display: flex;
    flex-direction: column;
    gap: 10px;
    align-items: center;
  }
  .app-body {
    padding: 24px;
    display: flex;
    flex-direction: column;
    gap: 20px;
    overflow: auto;
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
</style>
