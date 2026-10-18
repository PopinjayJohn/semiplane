import type { Identity, LoginBody } from './types.js';
import { request } from './client.js';

export type { Identity };

export async function login(body: LoginBody): Promise<Identity> {
  return request<Identity>('/api/v1/auth/login', {
    method: 'POST',
    body: JSON.stringify(body),
  });
}

export async function logout(): Promise<void> {
  await request<void>('/api/v1/auth/logout', { method: 'POST' });
}

export async function me(): Promise<Identity> {
  return request<Identity>('/api/v1/auth/me');
}
