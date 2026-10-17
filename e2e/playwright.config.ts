import { defineConfig, devices } from '@playwright/test';

// Thin slice: one smoke project. Full spec inventory lives in docs/e2e.md.
// Server lifecycle (build → per-worker port + scratch vault → health-wait)
// is owned by `make e2e`; BASE_URL is injected per worker.
export default defineConfig({
  testDir: './tests',
  timeout: 30_000,
  retries: process.env.CI ? 1 : 0,
  workers: process.env.CI ? 2 : undefined,
  use: {
    baseURL: process.env.BASE_URL ?? 'http://localhost:8080',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: 'retain-on-failure'
  },
  projects: [
    { name: 'smoke', testMatch: /smoke\/.*\.spec\.ts/, use: { ...devices['Desktop Chrome'] } },
    { name: 'full', testMatch: /full\/.*\.spec\.ts/, use: { ...devices['Desktop Chrome'] } }
  ]
});
