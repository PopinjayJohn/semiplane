// 03-wiki.spec.ts — Wave 1 wiki read surface, API-level against the real binary.
// GET note round-trip incl. ETag/304; 409 on empty-hash POST to existing path;
// search/graph served (player search shows no campaign/* rows); list endpoint.
// The scratch vault's FTS5 index is populated by `reindex` in run.sh.

import { expect, test } from '@playwright/test';
import { getNote, listNotes } from '../../helpers/vault';
import { loginAs as loginAsSession, logout } from '../../helpers/session';

test.beforeEach(async ({ page }) => {
  await page.goto('/');
});

test('GET note round-trip: content + fragment + meta + backlinks + ETag', async ({
  request,
}) => {
  const { sessionCookie } = await loginAsSession(request, 'player1');

  const doc = await getNote(request, sessionCookie, 'elora.md', 'player');
  expect(doc.status).toBe(200);
  expect(doc.path).toBe('elora.md');
  expect(typeof doc.content).toBe('string');
  expect(doc.content.length).toBeGreaterThan(0);
  expect(typeof doc.fragment).toBe('string');
  expect(doc.fragment).toContain('Elora');
  expect(doc.meta).toBeDefined();
  expect(Array.isArray(doc.backlinks)).toBe(true);
  expect(doc.etag).toMatch(/^[0-9a-f]+$/);
});

test('ETag / If-None-Match yields 304', async ({ request }) => {
  const { sessionCookie } = await loginAsSession(request, 'player1');

  const first = await getNote(request, sessionCookie, 'elora.md', 'player');
  expect(first.status).toBe(200);
  const etag = `"${first.etag}"`;

  const res = await request.get('/api/v1/notes/elora.md?role=player', {
    headers: {
      Cookie: `semiplane_session=${sessionCookie}`,
      'If-None-Match': etag,
    },
  });
  expect(res.status()).toBe(304);
});

test('409 on empty-hash POST to existing path (no silent overwrite)', async ({
  request,
}) => {
  const gm = await loginAsSession(request, 'gm');

  // elora.md already exists; GM tries empty-hash create → 409.
  const res = await request.post('/api/v1/notes/elora.md', {
    data: { content: '# overwritten', hash: '' },
    headers: { Cookie: `semiplane_session=${gm.sessionCookie}` },
  });
  expect(res.status()).toBe(409);
  expect(res.headers()['content-type']).toContain('application/problem+json');
});

test('update branch: non-empty matching hash succeeds (200), stale hash 409', async ({
  request,
}) => {
  const gm = await loginAsSession(request, 'gm');

  const doc = await getNote(request, gm.sessionCookie, 'elora.md', 'player');
  expect(doc.status).toBe(200);

  const updated = await request.post('/api/v1/notes/elora.md', {
    data: { content: '# Elora (edited)\n', hash: doc.etag },
    headers: { Cookie: `semiplane_session=${gm.sessionCookie}` },
  });
  expect(updated.status()).toBe(200);

  // Same hash again → stale → 409.
  const stale = await request.post('/api/v1/notes/elora.md', {
    data: { content: '# Elora (edited)\n', hash: doc.etag },
    headers: { Cookie: `semiplane_session=${gm.sessionCookie}` },
  });
  expect(stale.status()).toBe(409);
});

test('list endpoint returns notes with path + title', async ({ request }) => {
  const { sessionCookie } = await loginAsSession(request, 'player1');

  const all = await listNotes(request, sessionCookie);
  expect(Array.isArray(all.notes)).toBe(true);
  expect(all.notes.length).toBeGreaterThan(0);
  for (const n of all.notes) {
    expect(n.path).toMatch(/\.md$/);
    expect(typeof n.title).toBe('string');
  }

  const folder = await listNotes(request, sessionCookie, 'characters');
  expect(
    folder.notes.some((n) => n.path === 'characters/player1/throg.md'),
  ).toBeTruthy();
});

test('search returns ranked hits; player search shows no campaign/* rows', async ({
  request,
}) => {
  const { sessionCookie } = await loginAsSession(request, 'player1');

  // A term present in the seed vault (session-1 links to tavern).
  const res = await request.get('/api/v1/search?q=tavern', {
    headers: { Cookie: `semiplane_session=${sessionCookie}` },
  });
  expect(res.status()).toBe(200);
  const body = await res.json();
  expect(Array.isArray(body.hits)).toBe(true);
  expect(body.hits.length).toBeGreaterThan(0);
  for (const h of body.hits) {
    expect(h.path).toMatch(/\.md$/);
    expect(typeof h.snippet).toBe('string');
    expect(typeof h.score).toBe('number');
    // campaign/ is GM-only: no campaign/* rows for a player.
    expect(h.path).not.toMatch(/^campaign\//);
  }

  // Missing q → 400.
  const noQ = await request.get('/api/v1/search', {
    headers: { Cookie: `semiplane_session=${sessionCookie}` },
  });
  expect(noQ.status()).toBe(400);
});

test('graph returns nodes and edges', async ({ request }) => {
  const { sessionCookie } = await loginAsSession(request, 'player1');

  const res = await request.get('/api/v1/graph', {
    headers: { Cookie: `semiplane_session=${sessionCookie}` },
  });
  expect(res.status()).toBe(200);
  const body = await res.json();
  expect(Array.isArray(body.nodes)).toBe(true);
  expect(Array.isArray(body.edges)).toBe(true);
  expect(body.nodes.length).toBeGreaterThan(0);
  for (const n of body.nodes) {
    expect(n.path).toMatch(/\.md$/);
    expect(typeof n.title).toBe('string');
    // No campaign/* nodes for a player.
    expect(n.path).not.toMatch(/^campaign\//);
  }
  for (const e of body.edges) {
    expect(e.src).toMatch(/\.md$/);
    expect(e.dst).toMatch(/\.md$/);
  }
});
