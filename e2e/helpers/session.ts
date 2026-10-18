// Shared auth/session helpers for Wave-1 E2E specs (docs/e2e.md §G).
//
// All helpers operate through `page.request` (APIRequestContext). Cookies are
// NOT assumed to be shared between `page.request` and the browser context's
// cookie jar; instead the session cookie is extracted from the login
// response's Set-Cookie header and forwarded explicitly in the `Cookie`
// header of subsequent requests. This is reliable and explicit.
//
// Credentials (seeded by e2e/scripts/seedusers into the scratch vault):
//   gm        / gm-secret        (role: gm)
//   player1   / p1-secret        (role: player)
//   player2   / p2-secret        (role: player)
//
// LAN code seeded for player1: "123456" (single-use).
// Bearer token seeded for player1: "tok-rules-read-only" (scope: rules:read).
//
// No real credentials are ever used; fake OIDC via route.fulfill (helpers/oidc).
// zero external network calls.

import { type APIRequestContext, type Page } from '@playwright/test';

export const CREDENTIALS = {
  gm: { username: 'gm', password: 'gm-secret', role: 'gm' },
  player1: { username: 'player1', password: 'p1-secret', role: 'player' },
  player2: { username: 'player2', password: 'p2-secret', role: 'player' },
} as const;

export type Role = keyof typeof CREDENTIALS;

export const SESSION_COOKIE = 'semiplane_session';

// Extracts the session cookie value from a response's Set-Cookie header.
function sessionCookieFrom(res: { headers(): Record<string, string> }): string | null {
  const setCookie = res.headers()['set-cookie'];
  if (!setCookie) return null;
  const cookies = Array.isArray(setCookie) ? setCookie : [setCookie];
  for (const c of cookies) {
    const m = c.match(/^semiplane_session=([^;]+)/);
    if (m) return m[1];
  }
  return null;
}

// Headers carrying the session cookie extracted from a prior login response.
export function authHeaders(sessionCookie: string): Record<string, string> {
  return { Cookie: `${SESSION_COOKIE}=${sessionCookie}` };
}

// Log in as the given role via POST /api/v1/auth/login. Returns the decoded
// {user_id, role} body plus the session cookie string for forwarding.
export async function loginAs(
  request: APIRequestContext,
  role: Role,
): Promise<{ user_id: string; role: string; sessionCookie: string }> {
  const cred = CREDENTIALS[role];
  const res = await request.post('/api/v1/auth/login', {
    data: { username: cred.username, password: cred.password },
  });
  if (!res.ok()) {
    const text = await res.text();
    throw new Error(`login ${role} failed: ${res.status()} ${text}`);
  }
  const cookie = sessionCookieFrom(res);
  if (!cookie) throw new Error(`login ${role}: no session cookie set`);
  const body = (await res.json()) as { user_id: string; role: string };
  return { ...body, sessionCookie: cookie };
}

// Assert the Set-Cookie header from a login response carries HttpOnly +
// SameSite=Lax + Path=/. Secure depends on X-Forwarded-Proto / --force-secure;
// E2E runs plain HTTP so Secure is absent (tested at httptest level with XFP).
export async function expectCookieFlags(
  res: { headers(): Record<string, string> },
): Promise<void> {
  const setCookie = res.headers()['set-cookie'];
  const cookies = Array.isArray(setCookie) ? setCookie : [setCookie];
  const session = cookies.find((c) => c.startsWith(`${SESSION_COOKIE}=`));
  if (!session) throw new Error('session cookie not set after login');
  if (!session.includes('HttpOnly')) {
    throw new Error('session cookie missing HttpOnly');
  }
  if (!session.includes('SameSite=Lax')) {
    throw new Error(`session cookie missing SameSite=Lax: ${session}`);
  }
  if (!session.includes('Path=/')) {
    throw new Error(`session cookie missing Path=/`);
  }
}

// Add the session cookie to the browser context so that EventSource (and any
// page navigations) send it automatically. Call after loginAs when a spec
// needs SSE or page navigation authenticated.
export async function addSessionCookie(
  page: Page,
  sessionCookie: string,
): Promise<void> {
  // Add the cookie to the browser context so EventSource (withCredentials)
  // sends it automatically. url is derived from the page so it matches the
  // random port the server binds to.
  await page.context().addCookies([
    {
      name: SESSION_COOKIE,
      value: sessionCookie,
      url: page.url(),
    },
  ]);
}

// Log out via POST /api/v1/auth/logout with the session cookie. Always 204.
export async function logout(
  request: APIRequestContext,
  sessionCookie: string,
): Promise<void> {
  const res = await request.post('/api/v1/auth/logout', {
    headers: authHeaders(sessionCookie),
  });
  if (res.status() !== 204) {
    const text = await res.text();
    throw new Error(`logout failed: ${res.status()} ${text}`);
  }
}

// Consume a seeded LAN code via POST /api/v1/auth/lan/join. Returns {user_id, role}.
export async function joinLan(
  request: APIRequestContext,
  code: string,
): Promise<{ user_id: string; role: string }> {
  const res = await request.post('/api/v1/auth/lan/join', { data: { code } });
  if (!res.ok()) {
    const text = await res.text();
    throw new Error(`lan join ${code} failed: ${res.status()} ${text}`);
  }
  return (await res.json()) as { user_id: string; role: string };
}

// Mint a LAN code as GM via POST /api/v1/auth/lan. Returns {code}.
export async function createLan(
  request: APIRequestContext,
  sessionCookie: string,
): Promise<{ code: string }> {
  const res = await request.post('/api/v1/auth/lan', {
    headers: authHeaders(sessionCookie),
  });
  if (!res.ok()) {
    const text = await res.text();
    throw new Error(`lan create failed: ${res.status()} ${text}`);
  }
  return (await res.json()) as { code: string };
}

// Bearer token seeded for player1 with scope "rules:read".
export const PLAYER1_RULES_READ_TOKEN = 'tok-rules-read-only';
