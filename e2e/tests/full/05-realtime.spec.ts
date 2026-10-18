// 05-realtime.spec.ts — Wave 1 realtime SSE surface, API-level against the real binary.
// Verifies block_revealed/dice_rolled events are broadcast via SSE,
// Last-Event-ID replay works on reconnect, and reveal is 403 for player.
// Uses page.evaluate with the browser's Fetch API to read the SSE stream
// (EventSource with withCredentials proved unreliable in Playwright's cookie
// model); no DOM selectors, no waitForTimeout.
//
// ORDERING: open the SSE stream BEFORE triggering the server-side event.
// The SSE hub only delivers events broadcast after the connection opens
// (plus replay from Last-Event-ID).

import { expect, test, type Page } from '@playwright/test';
import {
  addSessionCookie,
  loginAs as loginAsSession,
} from '../../helpers/session';

// Reads the SSE stream via Fetch and returns events up to the first match
// of wantType (or timeout). The session cookie is sent explicitly in headers.
async function sseFetchEvents(
  page: Page,
  sessionCookie: string,
  wantType: string,
  opts?: { lastEventId?: number; timeoutMs?: number },
): Promise<{ id: string; type: string; data: string }[]> {
  const timeoutMs = opts?.timeoutMs ?? 8000;
  const url = new URL('/api/v1/events', page.url());
  if (opts?.lastEventId != null) {
    url.searchParams.set('lastEventId', String(opts.lastEventId));
  }

  const result = (await page.evaluate(
    async (args: any) => {
      const [u, c, wt, tms] = args as [string, string, string, number];
      const controller = new AbortController();
      const timeout = setTimeout(() => controller.abort(), tms);
      const received: { id: string; type: string; data: string }[] = [];

      try {
        const response = await fetch(u, {
          headers: { Cookie: `semiplane_session=${c}` },
          credentials: 'include',
          signal: controller.signal,
        });
        if (!response.body) return received;
        const reader = response.body.getReader();
        const decoder = new TextDecoder();
        let buffer = '';

        while (true) {
          const { done, value } = await reader.read();
          if (done) break;
          buffer += decoder.decode(value, { stream: true });

          const parts = buffer.split(/\n\n/);
          buffer = parts.pop() ?? '';
          for (const part of parts) {
            if (!part.trim()) continue;
            let id = '';
            let type = 'message';
            let data = '';
            for (const line of part.split('\n')) {
              if (line.startsWith('id: ')) id = line.slice(4);
              else if (line.startsWith('event: ')) type = line.slice(7);
              else if (line.startsWith('data: ')) data += line.slice(6) + '\n';
            }
            data = data.replace(/\n$/, '');
            received.push({ id, type, data });
            if (type === wt) {
              return received;
            }
          }
        }
      } catch {
        // Aborted (timeout) or connection closed — return what we have.
      } finally {
        clearTimeout(timeout);
      }
      return received;
    },
    [url.href, sessionCookie, wantType, timeoutMs],
  )) as { id: string; type: string; data: string }[];
  return result;
}

test.beforeEach(async ({ page }) => {
  await page.goto('/');
});

test('dice_rolled is broadcast on SSE after a roll', async ({
  page,
  request,
}) => {
  const gm = await loginAsSession(request, 'gm');
  await addSessionCookie(page, gm.sessionCookie);

  // Open SSE stream first, then roll.
  await sseFetchEvents(page, gm.sessionCookie, 'dice_rolled', {
    timeoutMs: 2000,
  }).catch(() => {
    /* no pending events yet — that's fine */
  });

  await request.post('/api/v1/dice/roll', {
    data: { formula: '1d20' },
    headers: { Cookie: `semiplane_session=${gm.sessionCookie}` },
  });

  const after = await sseFetchEvents(page, gm.sessionCookie, 'dice_rolled', {
    timeoutMs: 8000,
  });
  expect(
    after.some((e) => e.type === 'dice_rolled'),
    'expected a dice_rolled SSE event after the roll',
  ).toBeTruthy();
});

test('block_revealed is broadcast on SSE after a GM reveal', async ({
  page,
  request,
}) => {
  const gm = await loginAsSession(request, 'gm');
  await addSessionCookie(page, gm.sessionCookie);

  await sseFetchEvents(page, gm.sessionCookie, 'block_revealed', {
    timeoutMs: 2000,
  }).catch(() => {
    /* no pending events yet */
  });

  const res = await request.post('/api/v1/reveal', {
    data: { page: 'hidden-lore.md', blockId: 'b1' },
    headers: { Cookie: `semiplane_session=${gm.sessionCookie}` },
  });
  expect(res.status()).toBe(200);
  const body = await res.json();
  expect(body.ok).toBe(true);
  expect(typeof body.eventId).toBe('number');

  const revealed = await sseFetchEvents(page, gm.sessionCookie, 'block_revealed', {
    timeoutMs: 8000,
  });
  expect(
    revealed.some((e) => {
      try {
        const p = JSON.parse(e.data);
        return p.page === 'hidden-lore.md' && p.blockId === 'b1';
      } catch {
        return false;
      }
    }),
    'expected block_revealed payload for hidden-lore.md / b1',
  ).toBeTruthy();
});

test('Last-Event-ID replay on reconnect', async ({ page, request }) => {
  const gm = await loginAsSession(request, 'gm');
  await addSessionCookie(page, gm.sessionCookie);

  await sseFetchEvents(page, gm.sessionCookie, 'dice_rolled', {
    timeoutMs: 2000,
  }).catch(() => {
    /* no pending events yet */
  });

  await request.post('/api/v1/dice/roll', {
    data: { formula: '1d6' },
    headers: { Cookie: `semiplane_session=${gm.sessionCookie}` },
  });
  const first = await sseFetchEvents(page, gm.sessionCookie, 'dice_rolled', {
    timeoutMs: 8000,
  });
  const diceEvents = first.filter((e) => e.type === 'dice_rolled');
  expect(diceEvents.length).toBeGreaterThan(0);
  const lastId = Number(diceEvents[0].id);
  expect(lastId).toBeGreaterThan(0);

  await request.post('/api/v1/dice/roll', {
    data: { formula: '1d6' },
    headers: { Cookie: `semiplane_session=${gm.sessionCookie}` },
  });

  const replay = await sseFetchEvents(page, gm.sessionCookie, 'dice_rolled', {
    lastEventId: lastId,
    timeoutMs: 8000,
  });
  const replayedDice = replay.filter((e) => e.type === 'dice_rolled');
  expect(replayedDice.length).toBeGreaterThan(0);
  for (const e of replayedDice) {
    expect(Number(e.id)).toBeGreaterThan(lastId);
  }
});

test('reveal is 403 for player', async ({ request }) => {
  const p1 = await loginAsSession(request, 'player1');

  const res = await request.post('/api/v1/reveal', {
    data: { page: 'hidden-lore.md', blockId: 'b1' },
    headers: { Cookie: `semiplane_session=${p1.sessionCookie}` },
  });
  expect(res.status()).toBe(403);
  const body = await res.json();
  expect(body.title).toBe('Forbidden');
  const text = JSON.stringify(body);
  expect(text).not.toContain('hidden-lore');
  expect(text).not.toContain('b1');
});
