import type {
  GraphResponse,
  ListNotesResponse,
  NoteDoc,
  SearchResponse,
} from './types.js';
import { request } from './client.js';

// Multi-segment vault paths (e.g. campaign/intro.md) must have each segment
// encoded separately so the slashes survive in the request URL.
export function notePath(path: string): string {
  return path.split('/').map(encodeURIComponent).join('/');
}

function withQuery(base: string, params: URLSearchParams): string {
  const qs = params.toString();
  return qs ? `${base}?${qs}` : base;
}

export async function listNotes(
  folder?: string,
  prefix?: string
): Promise<ListNotesResponse> {
  const params = new URLSearchParams();
  if (folder !== undefined) params.set('folder', folder);
  if (prefix !== undefined) params.set('prefix', prefix);
  return request<ListNotesResponse>(withQuery('/api/v1/notes', params));
}

export async function getNote(
  path: string,
  role?: 'player' | 'gm'
): Promise<NoteDoc> {
  const params = new URLSearchParams();
  if (role !== undefined) params.set('role', role);
  return request<NoteDoc>(withQuery(`/api/v1/notes/${notePath(path)}`, params));
}

export async function postNote(
  path: string,
  content: string,
  hash: string,
  role?: 'player' | 'gm'
): Promise<NoteDoc> {
  const params = new URLSearchParams();
  if (role !== undefined) params.set('role', role);
  return request<NoteDoc>(withQuery(`/api/v1/notes/${notePath(path)}`, params), {
    method: 'POST',
    body: JSON.stringify({ content, hash }),
  });
}

export async function search(q: string, limit?: number): Promise<SearchResponse> {
  const params = new URLSearchParams({ q });
  if (limit !== undefined) params.set('limit', String(limit));
  return request<SearchResponse>(withQuery('/api/v1/search', params));
}

export async function graph(): Promise<GraphResponse> {
  return request<GraphResponse>('/api/v1/graph');
}
