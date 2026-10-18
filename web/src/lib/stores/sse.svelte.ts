/* web/src/lib/stores/sse.svelte.ts — SSE rune store (slice S4).
 *
 * Singleton EventSource wrapper for GET /api/v1/events.
 *   - Last-Event-ID persisted to localStorage and sent as ?lastEventId= on
 *     connect (EventSource also sends the Last-Event-ID header on reconnect)
 *   - automatic reconnect with backoff
 *   - per-type subscriptions (block_revealed, dice_rolled, …)
 *   - rune state: connected, lastEventId, events (capped log)
 *
 * Svelte-5 runes at module top level are allowed in .svelte.ts files and give
 * a shared reactive store any component can read. Import with the .svelte.ts
 * extension (tsconfig enables allowImportingTsExtensions).
 */

export type SseEvent =
  | {
      type: 'block_revealed';
      page: string;
      blockId: string;
      html: string;
      id: number;
    }
  | {
      type: 'dice_rolled';
      formula: string;
      total: number;
      faces: number[];
      seed: number;
      id: number;
    };

const STORAGE_KEY = 'semiplane:lastEventId';
const EVENT_CAP = 200;
const RECONNECT_MS = 2000;

let connected = $state(false);
let lastEventId = $state<number | null>(null);
let events = $state<SseEvent[]>([]);
const listeners = new Map<string, Set<(e: SseEvent) => void>>();
let es: EventSource | null = null;
let reconnectTimer: ReturnType<typeof setTimeout> | null = null;

function loadId(): number | null {
  try {
    const v = localStorage.getItem(STORAGE_KEY);
    return v ? Number(v) : null;
  } catch {
    return null;
  }
}

function saveId(id: number | null) {
  lastEventId = id;
  try {
    if (id == null) localStorage.removeItem(STORAGE_KEY);
    else localStorage.setItem(STORAGE_KEY, String(id));
  } catch {
    /* localStorage may be unavailable in private mode — degrade gracefully */
  }
}

function clearReconnect() {
  if (reconnectTimer) {
    clearTimeout(reconnectTimer);
    reconnectTimer = null;
  }
}

function close() {
  clearReconnect();
  if (es) {
    es.close();
    es = null;
  }
  connected = false;
}

function push(event: SseEvent) {
  events = [...events, event].slice(-EVENT_CAP);
}

function connect() {
  close();
  const id = loadId();
  const url = id ? `/api/v1/events?lastEventId=${id}` : '/api/v1/events';
  try {
    es = new EventSource(url);
    es.onopen = () => {
      connected = true;
    };
    es.onmessage = (ev: MessageEvent) => {
      const idStr = ev.lastEventId;
      if (idStr !== '') {
        const n = Number(idStr);
        if (!Number.isNaN(n)) saveId(n);
      }
      let data: unknown;
      try {
        data = JSON.parse(ev.data);
      } catch {
        data = ev.data;
      }
      const base = typeof data === 'object' && data !== null ? data : {};
      const event: SseEvent = {
        ...base,
        type: ev.type || 'message',
        id: (Number.isNaN(Number(idStr)) ? Date.now() : Number(idStr)) as number,
      } as SseEvent;
      push(event);
      const cbs = listeners.get(event.type);
      if (cbs) cbs.forEach((fn) => fn(event));
      // any other event type also notifies wildcard listeners
      const all = listeners.get('*');
      if (all) all.forEach((fn) => fn(event));
    };
    es.onerror = () => {
      connected = false;
      scheduleReconnect();
    };
  } catch {
    scheduleReconnect();
  }
}

function scheduleReconnect() {
  clearReconnect();
  reconnectTimer = setTimeout(connect, RECONNECT_MS);
}

export const sse = {
  get connected() {
    return connected;
  },
  get lastEventId() {
    return lastEventId;
  },
  get events() {
    return events;
  },
  /** Subscribe to events of `type` (or '*' for all). Returns an unsubscribe fn. */
  on(type: string, fn: (e: SseEvent) => void): () => void {
    let set = listeners.get(type);
    if (!set) {
      set = new Set();
      listeners.set(type, set);
    }
    set.add(fn);
    return () => {
      set.delete(fn);
    };
  },
  /** Open (or re-open) the EventSource connection. Idempotent. */
  connect() {
    connect();
  },
  /** Close the connection and stop reconnecting. */
  disconnect() {
    close();
  },
};

/* Auto-connect on module load so every client shares one SSE stream. */
connect();
