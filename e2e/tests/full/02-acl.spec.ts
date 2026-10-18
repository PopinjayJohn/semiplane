// 02-acl.spec.ts — Wave 1 ACL surface, API-level against the real binary.
// Default policy (docs/rules-ontology, internal/acl):
//   gm   read+write everything
//   player read everything except campaign/*; write only characters/<own-id>/*
//
// Credentials seeded by e2e/scripts/seedusers. API-level status-code
// assertions; session cookie forwarded explicitly. No waitForTimeout.

import { expect, test } from '@playwright/test';
import { createNote, getNote } from '../../helpers/vault';
import { loginAs as loginAsSession, logout } from '../../helpers/session';

test.beforeEach(async ({ page }) => {
  await page.goto('/');
});

test('player can write own characters/<id>/*, cannot write another player\'s', async ({
  request,
}) => {
  const { sessionCookie } = await loginAsSession(request, 'player1');

  // Own character file: 201 create.
  const own = await createNote(
    request,
    sessionCookie,
    'characters/player1/acl-test.md',
    '# Own Sheet\n\nHP 20.\n',
  );
  expect(own.path).toBe('characters/player1/acl-test.md');

  // Another player's character file: 403. Detail never names the target path.
  const other = await request.post(
    '/api/v1/notes/characters/player2/acl-test.md',
    { data: { content: '# nope', hash: '' }, headers: { Cookie: `semiplane_session=${sessionCookie}` } },
  );
  expect(other.status()).toBe(403);
  const body = await other.json();
  expect(body.title).toBe('Forbidden');
  const text = JSON.stringify(body);
  expect(text).not.toContain('player2');
  expect(text).not.toContain('acl-test.md');
});

test('player cannot read campaign/*; gm can', async ({ request }) => {
  // GM creates a campaign note.
  const gm = await loginAsSession(request, 'gm');
  const created = await createNote(
    request,
    gm.sessionCookie,
    'campaign/secret-note.md',
    '# GM Eyes Only\n\nThe dragon\'s true name.\n',
  );
  expect(created.path).toBe('campaign/secret-note.md');

  // GM reads it: 200.
  const gmGet = await getNote(request, gm.sessionCookie, 'campaign/secret-note.md', 'gm');
  expect(gmGet.status).toBe(200);
  expect(gmGet.fragment).toContain('dragon');

  // Player reads it: 403 (campaign/ is GM-only via default policy).
  await logout(request, gm.sessionCookie);
  const p1 = await loginAsSession(request, 'player1');
  const plGet = await getNote(request, p1.sessionCookie, 'campaign/secret-note.md', 'player');
  expect(plGet.status).toBe(403);
});

test('player reads public areas (rules/*) without ACL rejection', async ({
  request,
}) => {
  const { sessionCookie } = await loginAsSession(request, 'player1');

  // rules/ is public read for players (no grant needed). The note may not
  // exist in the seed vault; accept 404 (not 403) — the path is not denied
  // by ACL, it's simply absent.
  const res = await request.get('/api/v1/notes/rules/classes/barbarian.md', {
    headers: { Cookie: `semiplane_session=${sessionCookie}` },
  });
  expect(res.status()).not.toBe(403);
});

test('anonymous requests to vault routes are 401', async ({ request }) => {
  // No session cookie → every /api/v1/ route (except auth/version) is 401.
  const res = await request.get('/api/v1/notes/elora.md');
  expect(res.status()).toBe(401);
});
