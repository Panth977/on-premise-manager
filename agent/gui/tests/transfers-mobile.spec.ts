/** §21 #123 Files at 390 px: the list and its actions fit, no sideways scroll, the fs picker fits. */
import { test, expect } from './fixtures';
import { login, shot } from './helpers';
import type { Page } from '@playwright/test';

const SCR = process.env.OPM_SCREENS_DIR;
async function snap(page: Page, name: string, full = false) {
  await shot(page, name, full);
  if (SCR) await page.screenshot({ path: `${SCR}/${name}.png`, fullPage: full });
}
const noOverflow = async (page: Page) => expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(1);

test('390 px: the shared list fits; picker fits', async ({ page }) => {
  await login(page, '/_opm/app/app/transfers');
  const r = page.locator('[data-testid="file-row"][data-file="plant-layout.pdf"]');
  await expect(r).toHaveAttribute('data-badge', 'here');
  const box = (await r.boundingBox())!;
  expect(box.x + box.width).toBeLessThanOrEqual(391);
  for (const b of await r.getByRole('button').all()) {
    const bb = (await b.boundingBox())!;
    expect(bb.x + bb.width).toBeLessThanOrEqual(391);
  }
  await noOverflow(page);
  await snap(page, 'files-m', true);
  await page.getByTestId('publish-open').click();
  await page.getByTestId('publish-path').click();
  const pk = page.getByTestId('fs-picker');
  await expect(pk).toBeVisible();
  await pk.getByTestId('fs-path').fill('/var/log');
  await pk.getByTestId('fs-go').click();
  await pk.locator('[data-path="/var/log/syslog"] .name').click();
  await expect(pk.getByTestId('fs-selected')).toHaveText('/var/log/syslog');
  const pb = (await pk.boundingBox())!;
  expect(pb.x).toBeGreaterThanOrEqual(0);
  expect(pb.x + pb.width).toBeLessThanOrEqual(391);
  await noOverflow(page);
  await snap(page, 'fs-picker-m');
});
