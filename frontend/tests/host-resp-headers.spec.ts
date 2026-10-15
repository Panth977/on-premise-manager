import { test, expect } from '@playwright/test';
import { login } from './helpers';

/**
 * Regression (user report): Edit host → "Add response header" did nothing and the console showed
 * each_key_duplicate + effect_update_depth_exceeded (HostDrawer $effect read+wrote respKey → infinite loop).
 */
const EDGE = '/app/templates/TplNorthwindClient04/sites/site-01/edge';

test('host drawer: add three response headers, no runtime errors', async ({ page }) => {
  const errors: string[] = [];
  page.on('pageerror', (e) => errors.push(e.message));
  page.on('console', (m) => m.type() === 'error' && errors.push(m.text()));
  await page.setViewportSize({ width: 1440, height: 900 });
  await login(page, 'admin', EDGE);
  await page.getByRole('button', { name: /^Edit \*/ }).first().click();
  const drawer = page.getByTestId('host-drawer');
  await expect(drawer).toBeVisible();
  const headers: [string, string][] = [
    ['X-Frame-Options', 'DENY'],
    ['X-Content-Type-Options', 'nosniff'],
    ['Referrer-Policy', 'no-referrer'],
  ];
  const before = await drawer.locator('[data-testid^="host-resp-row-"]').count();
  for (const [i, [n, v]] of headers.entries()) {
    await page.getByTestId('host-resp-add').click();
    const idx = before + i;
    await expect(page.getByTestId(`host-resp-row-${idx}`)).toBeVisible();
    await page.getByTestId(`host-resp-name-${idx}`).fill(n);
    await page.keyboard.press('Escape');
    await page.getByTestId(`host-resp-value-${idx}-input`).fill(v);
  }
  await expect(drawer.locator('[data-testid^="host-resp-row-"]')).toHaveCount(before + 3);
  await page.getByTestId('host-apply').click();
  await expect(drawer).toBeHidden();
  await expect(page.getByTestId('host-resp-*')).toContainText(`${before + 3} headers`);
  expect(errors.filter((e) => /svelte\.dev\/e\/|Uncaught/.test(e)), errors.join('\n')).toEqual([]);
});
