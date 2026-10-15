/** §21 #100 Transfers at 390 px: Send stacks above Receive, no sideways scroll, the fs picker fits. */
import { test, expect } from './fixtures';
import { login, shot } from './helpers';
import type { Page } from '@playwright/test';

const SCR = process.env.OPM_SCREENS_DIR;
async function snap(page: Page, name: string, full = false) {
  await shot(page, name, full);
  if (SCR) await page.screenshot({ path: `${SCR}/${name}.png`, fullPage: full });
}
const noOverflow = async (page: Page) => expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(1);

test('390 px: Send first, then Receive; picker fits', async ({ page }) => {
  await login(page, '/_opm/app/app/transfers');
  const a = (await page.getByTestId('send-col').boundingBox())!;
  const b = (await page.getByTestId('receive-col').boundingBox())!;
  expect(a.y + a.height).toBeLessThanOrEqual(b.y + 1);
  await noOverflow(page);
  await snap(page, 'transfers-m', true);
  await page.getByTestId('send-open').click();
  await page.getByTestId('send-path').click();
  const pk = page.getByTestId('fs-picker');
  await expect(pk).toBeVisible();
  await pk.getByTestId('fs-path').fill('/var/log');
  await pk.getByTestId('fs-go').click();
  await pk.locator('[data-path="/var/log/syslog"] .name').click();
  await expect(pk.getByTestId('fs-selected')).toHaveText('/var/log/syslog');
  const box = (await pk.boundingBox())!;
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(391);
  await noOverflow(page);
  await snap(page, 'fs-picker-m');
});
