// Shared scratch-vault note helper for Wave-1 E2E specs (docs/e2e.md §G).
//
// Creates notes in the running server's scratch vault via POST
// /api/v1/notes/{path}. The empty-hash branch creates (201); the non-empty
// hash branch updates (200/409). Used to seed campaign/ content for ACL
// tests, wiki notes for search/graph tests, and gm-secret pages for reveal
// tests.
//
// The session cookie (from loginAs) is forwarded explicitly in the Cookie
// header. The vault filesystem is the source of truth; notes created here
// persist on disk in the scratch vault (never in fixtures/).

import { type APIRequestContext } from '@playwright/test';

export type NoteCreateResult = {
  path: string;
  content: string;
  fragment: string;
  meta: Record<string, unknown>;
  backlinks: string[];
  etag: string;
};

export function authHeaders(sessionCookie: string): Record<string, string> {
  return { Cookie: `semiplane_session=${sessionCookie}` };
}

// Create a note with the create branch (empty hash → 201, 409 if it exists).
export async function createNote(
  request: APIRequestContext,
  sessionCookie: string,
  path: string,
  content: string,
): Promise<NoteCreateResult> {
  const res = await request.post(`/api/v1/notes/${encodeURIComponent(path)}`, {
    data: { content, hash: '' },
    headers: authHeaders(sessionCookie),
  });
  if (res.status() === 409) {
    const body = await res.json();
    throw new Error(`note already exists: ${JSON.stringify(body)}`);
  }
  if (!res.ok()) {
    const text = await res.text();
    throw new Error(`create note ${path} failed: ${res.status()} ${text}`);
  }
  return (await res.json()) as NoteCreateResult;
}

// Update a note with the update branch (non-empty hash → 200, 409 on stale).
export async function updateNote(
  request: APIRequestContext,
  sessionCookie: string,
  path: string,
  content: string,
  hash: string,
): Promise<NoteCreateResult> {
  const res = await request.post(`/api/v1/notes/${encodeURIComponent(path)}`, {
    data: { content, hash },
    headers: authHeaders(sessionCookie),
  });
  if (!res.ok()) {
    const text = await res.text();
    throw new Error(`update note ${path} failed: ${res.status()} ${text}`);
  }
  return (await res.json()) as NoteCreateResult;
}

// Get a note document (GET /api/v1/notes/{path}?role=). Returns the NoteDoc
// wire shape: {path, content, fragment, meta, backlinks, etag}.
export async function getNote(
  request: APIRequestContext,
  sessionCookie: string,
  path: string,
  role: 'player' | 'gm' = 'player',
): Promise<NoteCreateResult & { status: number }> {
  const res = await request.get(
    `/api/v1/notes/${encodeURIComponent(path)}?role=${role}`,
    { headers: authHeaders(sessionCookie) },
  );
  return {
    status: res.status(),
    ...((await res.json()) as NoteCreateResult),
  };
}

// List notes under a prefix (GET /api/v1/notes?folder=).
export async function listNotes(
  request: APIRequestContext,
  sessionCookie: string,
  folder?: string,
): Promise<{ notes: Array<{ path: string; title: string }> }> {
  const url = new URL('/api/v1/notes', 'http://localhost');
  if (folder) url.searchParams.set('folder', folder);
  const res = await request.get(url.pathname + url.search, {
    headers: authHeaders(sessionCookie),
  });
  if (!res.ok()) {
    const text = await res.text();
    throw new Error(`list notes failed: ${res.status()} ${text}`);
  }
  return (await res.json()) as { notes: Array<{ path: string; title: string }> };
}
