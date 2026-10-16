import AxeBuilder from '@axe-core/playwright';
import { expect, type Page } from '@playwright/test';

// Shared axe gate (docs/e2e.md Accessibility): WCAG 2.2 AA tags, zero
// serious-or-worse violations. Usage in a spec:
//
//   await expectNoSeriousViolations(page);                    // whole page
//   await expectNoSeriousViolations(page, '[data-testid="wiki"]');  // one region
//
// Minor/best-practice findings do not fail — they get filed as issues, not
// ignored. Rule exclusions require a comment linking the issue.
export async function expectNoSeriousViolations(page: Page, include?: string) {
  let builder = new AxeBuilder({ page }).withTags([
    'wcag2a',
    'wcag2aa',
    'wcag21a',
    'wcag21aa',
    'wcag22a',
    'wcag22aa'
  ]);
  if (include) builder = builder.include(include);
  const results = await builder.analyze();
  const blocking = results.violations.filter((v) =>
    ['serious', 'critical'].includes(v.impact ?? '')
  );
  expect(
    blocking.map((v) => `${v.id}: ${v.nodes.length} node(s) — ${v.help}`),
    'axe serious/critical violations'
  ).toEqual([]);
}
