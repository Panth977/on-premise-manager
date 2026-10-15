import { test as base, expect } from '@playwright/test';

/** Every test fails on an uncaught page error, a console error or a Svelte runtime warning. */
export const test = base.extend<{ guard: void }>({
  guard: [
    async ({ page }, use) => {
      const problems: string[] = [];
      page.on('pageerror', (e) => problems.push(`pageerror: ${e.message}`));
      page.on('console', (m) => {
        const t = m.text();
        if (m.type() === 'error' && !/Failed to load resource|Not found: \/_opm\//.test(t)) problems.push(`console.error: ${t}`);
        if (m.type() === 'warning' && /svelte/i.test(t)) problems.push(`svelte warning: ${t}`);
      });
      await use();
      expect(problems, problems.join('\n')).toEqual([]);
    },
    { auto: true },
  ],
});
export { expect };
