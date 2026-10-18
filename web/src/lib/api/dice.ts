import type { RevealResponse, RollResult } from './types.js';
import { request } from './client.js';

export interface RollOptions {
  seed?: number;
  breakdown?: boolean;
}

export async function rollDice(
  formula: string,
  opts?: RollOptions
): Promise<RollResult> {
  const params = new URLSearchParams();
  if (opts?.breakdown) params.set('breakdown', '1');
  const body: Record<string, unknown> = { formula };
  if (opts?.seed !== undefined) body.seed = opts.seed;
  return request<RollResult>(withQuery('/api/v1/dice/roll', params), {
    method: 'POST',
    body: JSON.stringify(body),
  });
}

export async function reveal(
  page: string,
  blockId: string
): Promise<RevealResponse> {
  return request<RevealResponse>('/api/v1/reveal', {
    method: 'POST',
    body: JSON.stringify({ page, blockId }),
  });
}

function withQuery(base: string, params: URLSearchParams): string {
  const qs = params.toString();
  return qs ? `${base}?${qs}` : base;
}
