<script lang="ts">
  /* routes/Login.svelte — password login (slice S2).
   *
   * Strict-CSP clean: no inline handlers (onsubmit binds a script function),
   * no style= attributes, every interactive element carries data-test=.
   */
  import { session } from '../lib/stores/session.svelte.ts';

  let username = $state('');
  let password = $state('');
  let error = $state<string | null>(null);
  let submitting = $state(false);

  async function handleSubmit(event: SubmitEvent) {
    event.preventDefault();
    error = null;
    submitting = true;
    try {
      await session.login(username, password);
      window.location.replace('/');
    } catch {
      // api.login throws APIError on 401; unknown user and wrong password are
      // deliberately identical 401s (openapi.yaml), so show one generic message.
      error = 'Invalid username or password';
    } finally {
      submitting = false;
    }
  }
</script>

<section data-testid="login-page" class="login-page">
  <header class="login-header">
    <h1 data-testid="login-title" class="login-title">Sign in to Semiplane</h1>
  </header>

  <form data-testid="login-form" class="login-form" onsubmit={handleSubmit} novalidate>
    <div class="login-field">
      <label for="login-user">Username</label>
      <input
        id="login-user"
        data-testid="login-user"
        type="text"
        bind:value={username}
        required
        autocomplete="username"
      />
    </div>
    <div class="login-field">
      <label for="login-pass">Password</label>
      <input
        id="login-pass"
        data-testid="login-pass"
        type="password"
        bind:value={password}
        required
        autocomplete="current-password"
      />
    </div>
    {#if error}
      <p data-testid="login-error" role="alert" class="login-error">{error}</p>
    {/if}
    <button
      data-testid="login-submit"
      type="submit"
      disabled={submitting}
      class="login-submit"
    >
      {submitting ? 'Signing in…' : 'Sign in'}
    </button>
  </form>

  <p class="login-hint" data-testid="login-hint">
    OIDC and 6-digit LAN join are answered by the backend; this form is
    password login against POST /api/v1/auth/login.
  </p>
</section>

<style>
  .login-page {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    height: 100vh;
    padding: 16px;
  }
  .login-header {
    margin-bottom: 24px;
  }
  .login-title {
    margin: 0;
    font-size: 1.5rem;
    font-weight: 700;
    color: var(--sp-fg);
    letter-spacing: -0.01em;
  }
  .login-form {
    display: flex;
    flex-direction: column;
    gap: 12px;
    width: 100%;
    max-width: 320px;
  }
  .login-field {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .login-field label {
    font-size: 0.875rem;
    color: var(--sp-fg-muted);
  }
  .login-field input {
    padding: 8px 10px;
    border: 1px solid var(--sp-border);
    border-radius: var(--sp-radius-sm);
    background: var(--sp-bg-raised);
    color: var(--sp-fg);
    font: inherit;
  }
  .login-field input:focus-visible {
    outline: 3px solid var(--sp-accent);
    outline-offset: 2px;
    border-color: var(--sp-accent);
  }
  .login-error {
    margin: 0;
    font-size: 0.875rem;
    color: var(--sp-danger);
    min-height: 1.25rem;
  }
  .login-submit {
    margin-top: 8px;
    padding: 10px 16px;
    border: none;
    border-radius: var(--sp-radius);
    background: var(--sp-accent);
    color: var(--sp-accent-contrast);
    font: inherit;
    font-weight: 600;
    cursor: pointer;
  }
  .login-submit:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }
  .login-submit:focus-visible {
    outline: 3px solid var(--sp-accent);
    outline-offset: 2px;
  }
  .login-hint {
    margin-top: 20px;
    font-size: 0.75rem;
    color: var(--sp-fg-muted);
    text-align: center;
  }
</style>
