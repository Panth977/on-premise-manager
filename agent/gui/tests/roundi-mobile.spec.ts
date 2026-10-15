/** Round I at 390 px: the Agent page (one site, "*") fits without horizontal page scroll; dialogs + overlay fit. */
import { test, expect } from './fixtures';
import { knobs, login, open, shot } from './helpers';

const over = (page: import('@playwright/test').Page) => page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);

test('390 px: Agent page, upgrade dialog, restart overlay, Upgrade all', async ({ page }) => {
  test.setTimeout(90_000);
  await knobs(page, { 'opm.mock.restartSec': '3' });
  await login(page, '/_opm/app/app/agent');
  await expect(page.getByTestId('agent-cards')).toBeVisible();
  await page.waitForTimeout(300);
  expect(await over(page), 'overflow on app/agent').toBeLessThanOrEqual(1);
  await shot(page, 'i-agent-m');
  await page.getByTestId('upgrade-open').click();
  await page.getByTestId('upgrade-confirm').fill('app');
  await shot(page, 'i-upgrade-dialog-m', false);
  await page.getByTestId('upgrade-go').click();
  await expect(page.getByTestId('upgrade-progress')).toBeVisible();
  await page.getByTestId('upgrade-progress').scrollIntoViewIfNeeded();
  await shot(page, 'i-upgrade-progress-m', false);
  await expect(page.getByTestId('restart-overlay')).toBeVisible({ timeout: 15_000 });
  await shot(page, 'i-restart-overlay-m', false);
  await expect(page.getByTestId('upgrade-done')).toBeVisible({ timeout: 40_000 });
  expect(await over(page)).toBeLessThanOrEqual(1);
  await open(page, '/_opm/app/*/agent');
  await expect(page.getByTestId('upgrade-all-plan')).toBeVisible();
  await page.waitForTimeout(300);
  expect(await over(page), 'overflow on */agent').toBeLessThanOrEqual(1);
  await shot(page, 'i-agent-all-m');
});
