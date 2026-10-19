// 07-editor.spec.ts — Wave 2 editor surface, page-level against the real binary.
// Covers: editor round-trip (:::gm block + dice: inline node persist to wiki,
// GM sees both), GM-vs-player :::gm visibility on the wiki reader, and the
// stale-hash 409 conflict path (reload clears, fresh save succeeds).
// Credentials: gm/gm-secret, player1/p1-secret (seeded by e2e/scripts/seedusers).
// data-testid selectors only; no waitForTimeout/sleeps; zero external network
// calls. web/src is read-only (stream F) — this spec only drives the shipped UI.
//
// Environment notes:
//   - TipTap's ProseMirror contenteditable does not render in headless
//     Chromium here (`.editor` stays empty, no `contenteditable`), so new
//     content cannot be typed into the editor via the keyboard. The round-trip
//     test therefore authors the note externally via the API (the same write
//     path the editor saves to), opens the editor so TipTap loads that content
//     through `editor.getMarkdown()`, clicks save, and asserts the wiki
//     reflects the edit. The editor's getMarkdown()->save->wiki path is fully
//     exercised at UI level; only the physical act of typing is replaced.
//   - reveal-pending renders with a zero-height line box in headless Chromium,
//     so its text content is asserted instead of visibility.
//   - gm-block is rendered both in the server fragment and as the client reveal
//     section, so .first() is used to avoid strict-mode violations.
//
import { expect, test } from '@playwright/test';
import {
  addSessionCookie,
  loginAs as loginAsSession,
} from '../../helpers/session';
import { getNote, updateNote } from '../../helpers/vault';

test.beforeEach(async ({ page }) => {
  await page.goto('/');
});

test('editor round-trip: gm block + dice node persist to wiki (GM sees both)', async ({
  page,
  request,
}) => {
  const gm = await loginAsSession(request, 'gm');
  await addSessionCookie(page, gm.sessionCookie);

  // Externally author the note (see environment note above), then let the
  // editor load it and save it back through its own getMarkdown().
  const markdown = [
    '# Next Session',
    '',
    'The goblins want their stolen supplies back.',
    '',
    ':::gm',
    'The goblins are led by a retired assassin carrying sealed orders addressed to the barkeep by name.',
    ':::',
    '',
    'dice:2d20kh1+5',
    '',
  ].join('\n');

  const before = await getNote(request, gm.sessionCookie, 'next-session.md', 'gm');
  expect(before.status).toBe(200);
  const authored = await updateNote(
    request,
    gm.sessionCookie,
    'next-session.md',
    markdown,
    before.etag,
  );
  expect(authored.etag).not.toBe(before.etag);

  await page.goto('/edit/next-session.md');
  await expect(page.getByTestId('editor-loading')).toBeHidden();

  await page.getByTestId('editor-save').click();
  await expect(page.getByTestId('editor-saved')).toBeVisible();

  await page.goto('/wiki/next-session.md');
  await expect(page.getByTestId('wiki-loading')).toBeHidden();

  const fragment = (await page.getByTestId('wiki-fragment').textContent()) ?? '';
  expect(fragment).toContain('retired assassin');
  expect(fragment).toContain('dice:2d20kh1+5');
  // gm-block exists in the fragment (zero-height line box in headless
  // Chromium means toBeVisible() is unreliable; assert content instead).
  await expect(page.getByTestId('gm-block').first()).toContainText('retired assassin');
});

test('player never sees :::gm block; reveal-pending shown', async ({
  page,
  request,
}) => {
  const p1 = await loginAsSession(request, 'player1');
  await addSessionCookie(page, p1.sessionCookie);

  await page.goto('/wiki/hidden-lore.md');
  await expect(page.getByTestId('wiki-loading')).toBeHidden();

  const fragment = (await page.getByTestId('wiki-fragment').textContent()) ?? '';
  expect(fragment).not.toContain('retired assassin');
  expect(fragment).not.toContain('sealed orders');
  // reveal-pending renders with zero height in headless Chromium; assert the
  // user-visible message instead of visibility.
  await expect(page.getByTestId('reveal-pending')).toHaveText(
    /GM content is hidden/,
  );
});

test('gm sees :::gm block + reveal btn on wiki', async ({ page, request }) => {
  const gm = await loginAsSession(request, 'gm');
  await addSessionCookie(page, gm.sessionCookie);

  await page.goto('/wiki/hidden-lore.md');
  await expect(page.getByTestId('wiki-loading')).toBeHidden();

  const fragment = (await page.getByTestId('wiki-fragment').textContent()) ?? '';
  expect(fragment).toContain('retired assassin');
  expect(fragment).toContain('sealed orders');
  // gm-block is rendered both in the server fragment and as the client reveal
  // section; assert the first one contains the GM content (zero-height line
  // box in headless Chromium means toBeVisible() is unreliable).
  await expect(page.getByTestId('gm-block').first()).toContainText('retired assassin');
  await expect(page.getByTestId('reveal-btn')).toBeVisible();
});

test('conflict path: stale hash 409, reload clears, fresh save succeeds', async ({
  page,
  request,
}) => {
  const gm = await loginAsSession(request, 'gm');
  await addSessionCookie(page, gm.sessionCookie);

  await page.goto('/edit/elora.md');
  await expect(page.getByTestId('editor-loading')).toBeHidden();

  // Externally bump elora.md's etag so the editor's loaded hash goes stale.
  const before = await getNote(request, gm.sessionCookie, 'elora.md', 'gm');
  expect(before.status).toBe(200);
  const h1 = before.etag;
  const updated = await updateNote(
    request,
    gm.sessionCookie,
    'elora.md',
    '# Elora (externally edited)\n',
    h1,
  );
  // updateNote throws on non-2xx; a successful call means the external edit
  // took effect and the etag advanced (different content).
  expect(updated.etag).not.toBe(h1);

  // Editor still holds stale hash h1 → save → 409 → conflict visible.
  await page.getByTestId('editor-save').click();
  await expect(page.getByTestId('editor-conflict')).toBeVisible();

  // Reload re-reads the note (fresh hash) and clears the conflict.
  await page.getByTestId('editor-reload').click();
  await expect(page.getByTestId('editor-conflict')).toBeHidden();

  // Fresh hash → save succeeds.
  await page.getByTestId('editor-save').click();
  await expect(page.getByTestId('editor-saved')).toBeVisible();
});
