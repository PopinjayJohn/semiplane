// 01-auth.spec.ts — Wave 1 auth surface, API-level against the real binary.
// Credentials + seeded LAN code are provisioned by e2e/scripts/seedusers
// (gm/gm-secret, player1/p1-secret, player2/p2-secret, LAN code "123456"
// for player1).
//
// API-level: no DOM selectors. Session cookie extracted from Set-Cookie and
// forwarded explicitly in the Cookie header. No waitForTimeout for
// correctness. OIDC is unconfigured in E2E → the two OIDC routes answer 503
// (asserted directly, no fake OIDC needed for Wave 1).

import { expect, test } from '@playwright/test';
import {
  CREDENTIALS,
  createLan,
  expectCookieFlags,
  joinLan,
  loginAs,
  logout,
  SESSION_COOKIE,
} from '../../helpers/session';

test.beforeEach(async ({ page }) => {
  // Bind the cookie jar to the server's origin and give page.url() a base.
  await page.goto('/');
});

test('login happy path sets HttpOnly + SameSite=Lax session cookie', async ({
  request,
}) => {
  const { sessionCookie } = await loginAs(request, 'gm');

  // Assert flags from the Set-Cookie header returned by login.
  const res = await request.post('/api/v1/auth/login', {
    data: { username: CREDENTIALS.gm.username, password: CREDENTIALS.gm.password },
  });
  expect(res.status()).toBe(200);
  await expectCookieFlags(res);

  // /me with the cookie set by login returns the identity.
  const me = await request.get('/api/v1/auth/me', {
    headers: { Cookie: `${SESSION_COOKIE}=${sessionCookie}` },
  });
  expect(me.ok()).toBeTruthy();
  const meBody = await me.json();
  expect(meBody.user_id).toBe('gm');
  expect(meBody.role).toBe('gm');
});

test('login wrong password and unknown user are identical uninformative 401s', async ({
  request,
}) => {
  const wrong = await request.post('/api/v1/auth/login', {
    data: {
      username: CREDENTIALS.player1.username,
      password: 'wrong-password',
    },
  });
  expect(wrong.status()).toBe(401);
  expect(wrong.headers()['content-type']).toContain('application/problem+json');
  const wrongBody = await wrong.json();
  expect(wrongBody.title).toBe('Unauthorized');

  const unknown = await request.post('/api/v1/auth/login', {
    data: { username: 'nobody-here', password: 'wrong-password' },
  });
  expect(unknown.status()).toBe(401);
  const unknownBody = await unknown.json();
  // Same verdict, same shape: no account enumeration.
  expect(unknownBody.title).toBe(wrongBody.title);
  expect(unknown.status()).toBe(wrong.status());
});

test('login malformed body is 400', async ({ request }) => {
  const res = await request.post('/api/v1/auth/login', {
    data: { username: 7 as unknown as string, password: 'x' },
  });
  expect(res.status()).toBe(400);
  expect(res.headers()['content-type']).toContain('application/problem+json');
});

test('me without session is 401; with session is 200', async ({ request }) => {
  const noSession = await request.get('/api/v1/auth/me');
  expect(noSession.status()).toBe(401);

  const { sessionCookie } = await loginAs(request, 'player1');
  const me = await request.get('/api/v1/auth/me', {
    headers: { Cookie: `${SESSION_COOKIE}=${sessionCookie}` },
  });
  expect(me.status()).toBe(200);
  const body = await me.json();
  expect(body.user_id).toBe('player1');
  expect(body.role).toBe('player');
});

test('logout revokes the session; idempotent 204', async ({ request }) => {
  const { sessionCookie } = await loginAs(request, 'gm');

  const out = await request.post('/api/v1/auth/logout', {
    headers: { Cookie: `${SESSION_COOKIE}=${sessionCookie}` },
  });
  expect(out.status()).toBe(204);

  // Session cookie is cleared server-side: /me with it is 401.
  const me = await request.get('/api/v1/auth/me', {
    headers: { Cookie: `${SESSION_COOKIE}=${sessionCookie}` },
  });
  expect(me.status()).toBe(401);

  // Logout again: still 204 (idempotent, never leaks whether a cookie lived).
  const out2 = await request.post('/api/v1/auth/logout', {
    headers: { Cookie: `${SESSION_COOKIE}=${sessionCookie}` },
  });
  expect(out2.status()).toBe(204);
});

test('LAN code single-use: join works once, then 401', async ({ request }) => {
  // Join is unauthenticated by design (the code is the credential). Seeded
  // code "123456" is owned by player1.
  const join1 = await joinLan(request, '123456');
  expect(join1.user_id).toBe('player1');
  expect(join1.role).toBe('player');

  // Same code again → 401 (single-use, indistinguishable from wrong code).
  const join2 = await request.post('/api/v1/auth/lan/join', {
    data: { code: '123456' },
  });
  expect(join2.status()).toBe(401);
  const body = await join2.json();
  expect(body.title).toBe('Unauthorized');
});

// NOTE on ordering: this test must run after the single-use test above
// because both consume the shared per-IP LAN rate-limit budget (10/min).
// single-use spends 2 attempts; this test spends the remaining 8 (401) plus
// the 11th (429). Tests in this file run in order on one worker.
test('LAN join rate limit: wrong AND right codes both 429 over the limit', async ({
  request,
}) => {
  // 8 attempts remain in the per-IP budget (2 spent by single-use).
  for (let i = 0; i < 8; i++) {
    const res = await request.post('/api/v1/auth/lan/join', {
      data: { code: '000000' },
    });
    expect(res.status()).toBe(401);
  }
  // 11th attempt overall: rate limit exceeded → 429, even though the code
  // is wrong. The limiter runs before the hash comparison.
  const res = await request.post('/api/v1/auth/lan/join', {
    data: { code: '000000' },
  });
  expect(res.status()).toBe(429);
  const body = await res.json();
  expect(body.title).toBe('Too Many Requests');

  // A RIGHT code from the same (now blocked) address is ALSO 429: brute
  // force gains nothing, valid or not.
  const res2 = await request.post('/api/v1/auth/lan/join', {
    data: { code: '123456' },
  });
  expect(res2.status()).toBe(429);
});

test('LAN create: gm only, players 403, anonymous 401', async ({ request }) => {
  // Anonymous → 401.
  const anon = await request.post('/api/v1/auth/lan');
  expect(anon.status()).toBe(401);

  // Player → 403.
  const { sessionCookie } = await loginAs(request, 'player1');
  const pl = await request.post('/api/v1/auth/lan', {
    headers: { Cookie: `${SESSION_COOKIE}=${sessionCookie}` },
  });
  expect(pl.status()).toBe(403);

  // GM → 200 {code: 6 digits}.
  const gm = await loginAs(request, 'gm');
  const created = await createLan(request, gm.sessionCookie);
  expect(created.code).toMatch(/^\d{6}$/);
});

test('OIDC routes answer 503 when OIDC is not configured', async ({ request }) => {
  const start = await request.get('/api/v1/auth/oidc/start');
  expect(start.status()).toBe(503);

  const cb = await request.get('/api/v1/auth/oidc/callback?state=x&code=y');
  expect(cb.status()).toBe(503);
});
