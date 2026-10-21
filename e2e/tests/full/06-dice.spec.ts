// 06-dice.spec.ts — Wave 1 dice surface, API-level against the real binary.
// Default response omits breakdown; ?breakdown=1 includes kept/discarded/
// breakdown; seeded exact total+faces assertion via SEMIPLANE_TEST_SEED;
// invalid formula 400. Rule math is asserted in Go tests, never here — this
// spec asserts what the user sees (shape + seeded exactness).

import { expect, test } from '@playwright/test';
import { loginAs as loginAsSession } from '../../helpers/session';

// Deterministic values observed with SEMIPLANE_TEST_SEED=7357 (set by
// e2e/scripts/run.sh). These are engine outputs for this seed; if the seed
// changes these numbers change with it — update both together.
const SEEDED = {
  '1d20': { total: 17, faces: [17], seed: 7357 },
  '2d20kh1+5': {
    total: 22,
    faces: [17, 1],
    kept: [17],
    discarded: [1],
    seed: 7357,
  },
  // Variable formulas with SEMIPLANE_TEST_SEED=7357 and provided vars
  '1d12+@con': { total: 13, faces: [10], seed: 7357 }, // con=3, 1d12 rolls 10
  '2d20kh1+@mod': {
    total: 22,
    faces: [17, 1],
    kept: [17],
    discarded: [1],
    seed: 7357,
  }, // mod=5, 2d20kh1 rolls 17,1 -> kept 17 + 5 = 22
} as const;

test.beforeEach(async ({ page }) => {
  await page.goto('/');
});

test('default roll response omits breakdown', async ({ request }) => {
  const { sessionCookie } = await loginAsSession(request, 'player1');

  const res = await request.post('/api/v1/dice/roll', {
    data: { formula: '1d20' },
    headers: { Cookie: `semiplane_session=${sessionCookie}` },
  });
  expect(res.status()).toBe(200);
  const body = await res.json();
  expect(body.formula).toBe('1d20');
  expect(typeof body.total).toBe('number');
  expect(Array.isArray(body.faces)).toBe(true);
  expect(typeof body.seed).toBe('number');
  // kept / discarded / breakdown must be ABSENT without ?breakdown=1.
  expect(body.kept).toBeUndefined();
  expect(body.discarded).toBeUndefined();
  expect(body.breakdown).toBeUndefined();
});

test('?breakdown=1 includes kept/discarded/breakdown', async ({ request }) => {
  const { sessionCookie } = await loginAsSession(request, 'player1');

  const res = await request.post('/api/v1/dice/roll?breakdown=1', {
    data: { formula: '2d20kh1' },
    headers: { Cookie: `semiplane_session=${sessionCookie}` },
  });
  expect(res.status()).toBe(200);
  const body = await res.json();
  expect(body.formula).toBe('2d20kh1');
  expect(Array.isArray(body.kept)).toBe(true);
  expect(Array.isArray(body.discarded)).toBe(true);
  expect(Array.isArray(body.breakdown)).toBe(true);
  expect(body.kept.length).toBe(1);
  expect(body.discarded.length).toBe(1);
  expect(body.breakdown.length).toBeGreaterThan(0);
});

test('seeded exact total + faces via SEMIPLANE_TEST_SEED', async ({
  request,
}) => {
  const { sessionCookie } = await loginAsSession(request, 'player1');

  const res = await request.post('/api/v1/dice/roll', {
    data: { formula: '1d20' },
    headers: { Cookie: `semiplane_session=${sessionCookie}` },
  });
  expect(res.status()).toBe(200);
  const body = await res.json();
  expect(body.seed).toBe(SEEDED['1d20'].seed);
  expect(body.total).toBe(SEEDED['1d20'].total);
  expect(body.faces).toEqual(SEEDED['1d20'].faces);

  // Same formula + same seed → same result (deterministic replay).
  const res2 = await request.post('/api/v1/dice/roll', {
    data: { formula: '1d20' },
    headers: { Cookie: `semiplane_session=${sessionCookie}` },
  });
  const body2 = await res2.json();
  expect(body2.total).toBe(body.total);
  expect(body2.faces).toEqual(body.faces);
  expect(body2.seed).toBe(body.seed);
});

test('breakdown=1 seeded exact kept/discarded', async ({ request }) => {
  const { sessionCookie } = await loginAsSession(request, 'player1');

  const res = await request.post('/api/v1/dice/roll?breakdown=1', {
    data: { formula: '2d20kh1+5' },
    headers: { Cookie: `semiplane_session=${sessionCookie}` },
  });
  expect(res.status()).toBe(200);
  const body = await res.json();
  expect(body.total).toBe(SEEDED['2d20kh1+5'].total);
  expect(body.faces).toEqual(SEEDED['2d20kh1+5'].faces);
  expect(body.kept).toEqual(SEEDED['2d20kh1+5'].kept);
  expect(body.discarded).toEqual(SEEDED['2d20kh1+5'].discarded);
  expect(Array.isArray(body.breakdown)).toBe(true);
  expect(body.breakdown.length).toBeGreaterThan(0);
});

test('variable substitution @con', async ({ request }) => {
  const { sessionCookie } = await loginAsSession(request, 'player1');

  const res = await request.post('/api/v1/dice/roll', {
    data: { formula: '1d12+@con', vars: { con: 3 } },
    headers: { Cookie: `semiplane_session=${sessionCookie}` },
  });
  expect(res.status()).toBe(200);
  const body = await res.json();
  expect(body.seed).toBe(SEEDED['1d12+@con'].seed);
  expect(body.total).toBe(SEEDED['1d12+@con'].total);
  expect(body.faces).toEqual(SEEDED['1d12+@con'].faces);
  expect(body.formula).toBe('1d12+@con');
});

test('variable in keep expression @mod', async ({ request }) => {
  const { sessionCookie } = await loginAsSession(request, 'player1');

  const res = await request.post('/api/v1/dice/roll?breakdown=1', {
    data: { formula: '2d20kh1+@mod', vars: { mod: 5 } },
    headers: { Cookie: `semiplane_session=${sessionCookie}` },
  });
  expect(res.status()).toBe(200);
  const body = await res.json();
  expect(body.seed).toBe(SEEDED['2d20kh1+@mod'].seed);
  expect(body.total).toBe(SEEDED['2d20kh1+@mod'].total);
  expect(body.faces).toEqual(SEEDED['2d20kh1+@mod'].faces);
  expect(body.kept).toEqual(SEEDED['2d20kh1+@mod'].kept);
  expect(body.discarded).toEqual(SEEDED['2d20kh1+@mod'].discarded);
  expect(Array.isArray(body.breakdown)).toBe(true);
  expect(body.breakdown.length).toBeGreaterThan(0);
});

test('unknown variable is 400', async ({ request }) => {
  const { sessionCookie } = await loginAsSession(request, 'player1');

  const res = await request.post('/api/v1/dice/roll', {
    data: { formula: '1d20+@unknown', vars: { known: 5 } },
    headers: { Cookie: `semiplane_session=${sessionCookie}` },
  });
  expect(res.status()).toBe(400);
  expect(res.headers()['content-type']).toContain('application/problem+json');
});

test('negative variable substitution', async ({ request }) => {
  const { sessionCookie } = await loginAsSession(request, 'player1');

  const res = await request.post('/api/v1/dice/roll', {
    data: { formula: '1d20-@penalty', vars: { penalty: 2 } },
    headers: { Cookie: `semiplane_session=${sessionCookie}` },
  });
  expect(res.status()).toBe(200);
  const body = await res.json();
  // With SEMIPLANE_TEST_SEED=7357, 1d20 rolls 17
  // Total = 17 - 2 = 15
  expect(body.total).toBe(15);
  expect(body.faces).toEqual([17]);
  expect(body.formula).toBe('1d20-@penalty');
});

test('invalid formula is 400', async ({ request }) => {
  const { sessionCookie } = await loginAsSession(request, 'player1');

  const res = await request.post('/api/v1/dice/roll', {
    data: { formula: 'not dice at all ((((' },
    headers: { Cookie: `semiplane_session=${sessionCookie}` },
  });
  expect(res.status()).toBe(400);
  expect(res.headers()['content-type']).toContain('application/problem+json');
});

test('missing formula is 400', async ({ request }) => {
  const { sessionCookie } = await loginAsSession(request, 'player1');

  const res = await request.post('/api/v1/dice/roll', {
    data: {},
    headers: { Cookie: `semiplane_session=${sessionCookie}` },
  });
  expect(res.status()).toBe(400);
});
