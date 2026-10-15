/**
 * Screenshots of the variable / secret / file drawers, the deployment edit modal and the activities filters
 * (light, dark, 390 px) → frontend/screenshots/ux-mock-config-*.png. Skipped unless OPM_SCREENSHOTS=1.
 */
import { test, expect, type Page } from '@playwright/test';
import { login, SEED } from './helpers';

test.skip(!process.env.OPM_SCREENSHOTS, 'set OPM_SCREENSHOTS=1');

const SHOTS = 'screenshots';
const T = `/app/templates/${SEED.template}`;
const D = `/app/deployments/${SEED.deployment}`;
const dark = (p: Page) => p.evaluate(() => document.documentElement.setAttribute('data-theme', 'dark'));

async function shots(page: Page, sfx: string, theme: (p: Page) => Promise<void>) {
  await page.goto(`${T}/variables`);
  await theme(page);
  await page.getByTestId('add-variable').click();
  await page.getByTestId('var-label').fill('Redis port');
  await page.getByTestId('var-type-number').click();
  await page.getByTestId('var-min').fill('1024');
  await page.screenshot({ path: `${SHOTS}/ux-mock-config-variable-${sfx}.png` });
  await page.keyboard.press('Escape');
  await page.goto(`${T}/secrets`);
  await theme(page);
  await page.getByTestId('add-secret').click();
  await page.getByTestId('sec-label').fill('DB password');
  await page.getByTestId('sec-generate').click();
  await expect(page.getByTestId('sec-charset')).toBeVisible();
  await page.waitForTimeout(400);
  await page.screenshot({ path: `${SHOTS}/ux-mock-config-secret-${sfx}.png` });
  await page.keyboard.press('Escape');
  await page.goto(`${T}/files`);
  await theme(page);
  await page.getByTestId('add-file').click();
  await page.getByTestId('file-label').fill('Site logo');
  await page.getByTestId('file-mime').click();
  await page.screenshot({ path: `${SHOTS}/ux-mock-config-file-mime-${sfx}.png` });
  await page.keyboard.press('Escape');
  await page.getByTestId('file-unit-KiB').click();
  await page.screenshot({ path: `${SHOTS}/ux-mock-config-file-${sfx}.png` });
  await page.keyboard.press('Escape');
  await page.goto(D);
  await theme(page);
  await page.getByTestId('edit-deployment').click();
  await expect(page.getByTestId('deployment-drawer')).toBeVisible();
  await page.waitForTimeout(400);
  await page.screenshot({ path: `${SHOTS}/ux-mock-config-deployment-edit-${sfx}.png` });
  await page.keyboard.press('Escape');
  await page.goto(`${D}/activities`);
  await theme(page);
  await expect(page.getByTestId('activity-row').first()).toBeVisible();
  await page.getByTestId('f-actor').click();
  await page.screenshot({ path: `${SHOTS}/ux-mock-config-activities-${sfx}.png` });
}

test('config forms light', async ({ page }) => {
  await login(page, 'admin', `${T}/variables`);
  await shots(page, 'light', async () => {});
});
test('config forms dark', async ({ page }) => {
  await login(page, 'admin', `${T}/variables`);
  await shots(page, 'dark', dark);
});
test('config forms 390 px', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await login(page, 'admin', `${T}/variables`);
  await shots(page, '390', async () => {});
});
