import { expect, test } from '@playwright/test';

// Smoke: the binary serves the SPA shell and the health endpoint.
// Rule math is never asserted here (docs/e2e.md) — only that the surface loads.
test('app shell loads', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByTestId('app-title')).toHaveText('Semiplane');
});

test('health endpoint is ok', async ({ request }) => {
  const res = await request.get('/healthz');
  expect(res.ok()).toBeTruthy();
  const body = await res.json();
  expect(body.status).toBe('ok');
});

test('health visible in UI', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByTestId('health')).toContainText('backend: ok');
});
