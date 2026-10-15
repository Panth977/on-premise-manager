/**
 * §21 #100 Transfers: Send + Receive merged into one two-column page (left = Send cloud → site, right = Receive),
 * store-and-forward states (fetchedAt), old routes redirect, sidebar + Sites table link, permissions.
 * Optional screenshots (OPM_TX_SHOTS=<dir>): light, dark, 390 px.
 */
import { test, expect, type Page } from '@playwright/test';
import { login, SEED } from './helpers';

const D = `/app/deployments/${SEED.deployment}`;
const SHOTS = process.env.OPM_TX_SHOTS;

test.describe('transfers (§21 #100)', () => {
  test('two columns: Send left, Receive right, store-and-forward states', async ({ page }) => {
    await login(page, 'admin', `${D}/sites/app/transfers`);
    const send = page.getByTestId('col-send');
    const recv = page.getByTestId('col-receive');
    await expect(send).toContainText('Send to app');
    await expect(recv).toContainText('Received from app');
    const sb = (await send.boundingBox())!;
    const rb = (await recv.boundingBox())!;
    expect(sb.x).toBeLessThan(rb.x);
    expect(Math.abs(sb.y - rb.y)).toBeLessThan(2);
    // Send column (cloud → site)
    const row = (name: string) => send.getByTestId('transfer-row').filter({ hasText: name });
    await expect(row('firmware-2.4.1.bin')).toHaveAttribute('data-state', 'pending');
    await expect(row('firmware-2.4.1.bin')).toContainText('the agent fetches it the next time it is online');
    await expect(row('calibration.csv')).toHaveAttribute('data-state', 'on-site');
    await expect(row('calibration.csv')).toContainText('fetched');
    await expect(row('license.txt')).toHaveAttribute('data-state', 'saved-on-server');
    await expect(row('license.txt')).toContainText('/opt/acme/license.txt');
    // Receive column (site → cloud)
    const rrow = (name: string) => recv.getByTestId('receive-row').filter({ hasText: name });
    await expect(rrow('app-2026-10-08.log')).toContainText('arriving from the site');
    await expect(rrow('app-2026-10-08.log').getByTestId('download')).toHaveCount(0);
    await expect(rrow('pg_dump.sql.gz')).toHaveAttribute('data-state', 'stored');
    const dl = page.waitForEvent('download');
    await rrow('pg_dump.sql.gz').getByTestId('download').click();
    expect((await dl).suggestedFilename()).toBe('pg_dump.sql.gz');
    await expect(rrow('pg_dump.sql.gz')).toHaveAttribute('data-state', 'downloaded');
  });

  test('old Send / Receive routes redirect to Transfers (query kept)', async ({ page }) => {
    await login(page, 'admin', D);
    await page.goto(`${D}/sites/app/send?x=1`);
    await expect(page).toHaveURL(new RegExp(`${D}/sites/app/transfers\\?x=1$`));
    await expect(page.getByTestId('col-send')).toBeVisible();
    await page.goto(`${D}/sites/db/receive`);
    await expect(page).toHaveURL(new RegExp(`${D}/sites/db/transfers$`));
    await expect(page.getByTestId('receive-row').first()).toContainText('redis-dump');
  });

  test('sidebar tree + Sites table link to Transfers', async ({ page }) => {
    await login(page, 'admin', `${D}/sites`);
    await page.locator('[data-testid=site-transfers][href$="/sites/app/transfers"]').click();
    await expect(page).toHaveURL(/\/sites\/app\/transfers$/);
    await expect(page.getByTestId('tree-app-transfers')).toHaveClass(/on/);
  });

  test('without deployment.transfers.read: no access state, no tree link', async ({ page }) => {
    await login(page, 'viewer', `${D}/sites/app/transfers`);
    await expect(page.getByText('You need deployment.transfers.read')).toBeVisible();
    await expect(page.getByTestId('send-file')).toHaveCount(0);
    await expect(page.getByTestId('tree-app-transfers')).toHaveCount(0);
  });

  test('mobile 390 px: columns stack, Send first', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await login(page, 'admin', `${D}/sites/app/transfers`);
    const sb = (await page.getByTestId('col-send').boundingBox())!;
    const rb = (await page.getByTestId('col-receive').boundingBox())!;
    expect(sb.y).toBeLessThan(rb.y);
    expect(sb.width).toBeLessThanOrEqual(390);
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(overflow).toBeLessThanOrEqual(0);
  });

  test('screenshots (light, dark, 390 px)', async ({ page }) => {
    test.skip(!SHOTS, 'set OPM_TX_SHOTS=<dir>');
    const shot = async (p: Page, name: string) => {
      await p.waitForTimeout(400);
      await p.screenshot({ path: `${SHOTS}/${name}.png`, fullPage: true });
    };
    await page.setViewportSize({ width: 1440, height: 900 });
    await login(page, 'admin', `${D}/sites/app/transfers`);
    await expect(page.getByTestId('transfer-row').first()).toBeVisible();
    await page.emulateMedia({ colorScheme: 'light' });
    await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'light'));
    await shot(page, 'cloud-transfers-light');
    await page.getByTestId('send-file').click();
    await shot(page, 'cloud-transfers-send-modal');
    await page.keyboard.press('Escape');
    await page.emulateMedia({ colorScheme: 'dark' });
    await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'dark'));
    await shot(page, 'cloud-transfers-dark');
    await page.setViewportSize({ width: 390, height: 844 });
    await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'light'));
    await page.emulateMedia({ colorScheme: 'light' });
    await shot(page, 'cloud-transfers-mobile');
  });
});
