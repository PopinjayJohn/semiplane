import type { Problem } from './types.js';

/**
 * RFC 7807 problem response surfaced as a thrown error.
 * Non-ok responses always carry application/problem+json bodies; when a
 * non-ok body cannot be parsed we synthesise a minimal problem so the error
 * shape is never opaque.
 */
export class APIError extends Error {
  readonly status: number;
  readonly problem: Problem;

  constructor(status: number, problem: Problem, message: string) {
    super(message);
    this.status = status;
    this.problem = problem;
  }
}

function headersToRecord(headers?: RequestInit['headers']): Record<string, string> {
  if (!headers) return {};
  if (headers instanceof Headers) {
    const record: Record<string, string> = {};
    headers.forEach((value, key) => {
      record[key] = value;
    });
    return record;
  }
  if (Array.isArray(headers)) return Object.fromEntries(headers);
  return { ...headers };
}

/**
 * Typed fetch against the same-origin Semiplane API (base path '').
 * Every call sends the session cookie (`credentials: 'include'`) and speaks
 * JSON. SSE (GET /api/v1/events) is not handled here — S4 uses EventSource.
 */
export async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const headers: Record<string, string> = {
    Accept: 'application/json',
    ...headersToRecord(init?.headers),
  };
  if (init?.body !== undefined) {
    headers['Content-Type'] = 'application/json';
  }

  const res = await fetch(path, {
    ...init,
    credentials: 'include',
    headers,
  });

  if (!res.ok) {
    let problem: Problem;
    try {
      problem = (await res.json()) as Problem;
    } catch {
      problem = {
        type: 'about:blank',
        title: res.statusText,
        status: res.status,
      };
    }
    throw new APIError(
      res.status,
      problem,
      `${res.status} ${problem.title ?? res.statusText}`
    );
  }

  if (res.status === 204) return undefined as unknown as T;
  const text = await res.text();
  if (!text) return undefined as unknown as T;
  return JSON.parse(text) as T;
}
