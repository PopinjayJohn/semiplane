// SSE helper for Wave-1 E2E specs (docs/e2e.md §G.
//
// Connects a browser EventSource to GET /api/v1/events and resolves with all
// events received up to and including the first event whose type matches
// `wantType` (or a timeout). Supports `?lastEventId=` catch-up for
// reconnect/replay testing.
//
// Uses EventSource (the real browser SSE transport) with `withCredentials:
// true` so the session cookie (set via helpers/session.addSessionCookie) is
// sent automatically. The Promise inside `page.evaluate` always resolves
// within `timeoutMs` via a setTimeout fallback, so the helper never hangs.
//
// Event ids are server-assigned monotonic sequence numbers, delivered as the
// SSE `id:` field (surfaced via ev.lastEventId). The `event:` type is surfaced
// via ev.type (Chromium implements this for native EventSource).
//
// Usage:
//
//	await addSessionCookie(page, sessionCookie);
//	const events = await sseWaitFor(page, 'dice_rolled');
//	// connect first, then trigger:
//	await request.post('/api/v1/dice/roll', ...);
//
// Replay:
//
//	const first = await sseWaitFor(page, 'dice_rolled');
//	const lastId = Number(first.find((e) => e.type === 'dice_rolled')?.id);
//	const replay = await sseWaitFor(page, 'dice_rolled', {
//	  lastEventId: lastId,
//	});
//
// No data-testid is involved: the SSE transport itself is anonymous; assertions
// are on the parsed event payload (what the user sees).

import { type Page } from '@playwright/test';

export type SSEvent = { id: string; type: string; data: string };

// sseWaitFor opens an EventSource to /api/v1/events and resolves with all
// events received up to and including the first event whose type matches
// wantType. On timeout it resolves with whatever was received.
//
// Connect the EventSource BEFORE triggering the server-side event, since
// EventSource only sees events broadcast after it connects (plus replay from
// Last-Event-ID).
export async function sseWaitFor(
  page: Page,
  wantType: string,
  opts?: {
    lastEventId?: number;
    timeoutMs?: number;
  },
): Promise<SSEvent[]> {
  const timeoutMs = opts?.timeoutMs ?? 10_000;
  const url = new URL('/api/v1/events', page.url());
  if (opts?.lastEventId != null) {
    url.searchParams.set('lastEventId', String(opts.lastEventId));
  }

  const result = await page.evaluate(
    async (args: any) => {
      const [u, wt, tms] = args as [string, string, number];
      return new Promise((resolve) => {
        const es = new EventSource(u, { withCredentials: true });
        const received: SSEvent[] = [];
        let resolved = false;

        const timer = setTimeout(() => {
          if (!resolved) {
            resolved = true;
            es.close();
            resolve({ events: received });
          }
        }, tms);

        es.onmessage = (ev: MessageEvent) => {
          if (resolved) return;
          received.push({
            id: String(ev.lastEventId ?? ''),
            type: ev.type || 'message',
            data: ev.data,
          });
          if (ev.type === wt) {
            resolved = true;
            clearTimeout(timer);
            es.close();
            resolve({ events: received });
          }
        };

        es.onerror = () => {
          if (!resolved) {
            resolved = true;
            clearTimeout(timer);
            es.close();
            resolve({ events: received, error: 'EventSource error' });
          }
        };
      });
    },
    [url.href, wantType, timeoutMs],
  ) as { events: SSEvent[]; error?: string };

  const events = (result.events as SSEvent[]).map((e) => ({
    id: e.id,
    type: e.type,
    data: e.data,
  }));

  if (result.error && events.length === 0) {
    throw new Error(`EventSource failed: ${result.error}`);
  }

  return events;
}
