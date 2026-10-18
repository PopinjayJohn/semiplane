// 04-gm-secrets.spec.ts — Wave 1 GM-secret surface, API-level against the real binary.
// role=player strips :::gm server-side; role=gm keeps it; bad role 400.
// This is a server-side property: the fragment is pre-rendered by the server,
// never client-side hidden (security property from internal/render).

import { expect, test } from '@playwright/test';
import { getNote } from '../../helpers/vault';
import { loginAs as loginAsSession } from '../../helpers/session';

test.beforeEach(async ({ page }) => {
  await page.goto('/');
});

test('role=player strips :::gm from the fragment', async ({ request }) => {
  const { sessionCookie } = await loginAsSession(request, 'player1');

  const doc = await getNote(request, sessionCookie, 'hidden-lore.md', 'player');
  expect(doc.status).toBe(200);
  expect(doc.fragment).not.toContain('retired assassin');
  expect(doc.fragment).not.toContain('sealed orders');
  // The gm-block marker element must not be present.
  expect(doc.fragment).not.toContain('data-testid="gm-block"');
  // Public body text remains.
  expect(doc.fragment).toContain('Hidden Lore');
  expect(doc.fragment).toContain('cellar door');
});

test('role=gm keeps :::gm in the fragment', async ({ request }) => {
  const { sessionCookie } = await loginAsSession(request, 'gm');

  const doc = await getNote(request, sessionCookie, 'hidden-lore.md', 'gm');
  expect(doc.status).toBe(200);
  expect(doc.fragment).toContain('retired assassin');
  expect(doc.fragment).toContain('sealed orders');
  // The gm-block is rendered as a server-side gated block with a testid.
  expect(doc.fragment).toContain('data-testid="gm-block"');
  expect(doc.fragment).toContain('gm-block');
});

test('bad role is 400 (server-side property)', async ({ request }) => {
  const { sessionCookie } = await loginAsSession(request, 'player1');

  const res = await request.get('/api/v1/notes/hidden-lore.md?role=dm', {
    headers: { Cookie: `semiplane_session=${sessionCookie}` },
  });
  expect(res.status()).toBe(400);
  expect(res.headers()['content-type']).toContain('application/problem+json');
  const body = await res.json();
  expect(body.title).toBe('bad request');
});
