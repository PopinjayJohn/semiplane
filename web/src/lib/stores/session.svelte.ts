/* web/src/lib/stores/session.svelte.ts — Svelte-5-runes session store (slice S2).
 *
 * Holds the authenticated identity ({user_id, role}) minted by the
 * /api/v1/auth/* routes and carried by the `semiplane_session` cookie.
 * All API calls go through web/src/lib/api/auth (credentials: 'include').
 *
 * Runes at module top level are allowed in .svelte.ts files and give a
 * singleton reactive store any component can read.
 */

import {
  login as apiLogin,
  logout as apiLogout,
  me as apiMe,
  type Identity,
} from '../api/auth.js';

let identity = $state<Identity | null>(null);
let loaded = $state(false);

export const session = {
  /** Resolved identity, or null when unauthenticated. */
  get identity(): Identity | null {
    return identity;
  },
  /** True once a session is established. */
  get isAuthenticated(): boolean {
    return identity !== null;
  },
  /** 'gm' | 'player' | null — drives role-scoped UI (GmBlock, creator, …). */
  get role(): 'gm' | 'player' | null {
    return identity?.role ?? null;
  },
  /** True after init() has settled (cookie restored or proven absent). */
  get loaded(): boolean {
    return loaded;
  },

  /** Restore the session from the cookie on boot; null on 401. */
  async init(): Promise<void> {
    try {
      identity = await apiMe();
    } catch {
      identity = null;
    }
    loaded = true;
  },

  /** Password login: sets the session cookie and updates identity. */
  async login(username: string, password: string): Promise<void> {
    identity = await apiLogin({ username, password });
    loaded = true;
  },

  /** Revoke the session, clear the cookie, drop identity. */
  async logout(): Promise<void> {
    try {
      await apiLogout();
    } catch {
      /* logout is idempotent; ignore network errors on the way out */
    }
    identity = null;
    loaded = true;
  },

  /** Re-read identity from the cookie (e.g. after a role change). */
  async refresh(): Promise<void> {
    try {
      identity = await apiMe();
    } catch {
      identity = null;
    }
  },
};
