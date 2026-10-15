// §21 #104 Branch at 390 px: no horizontal scroll; overview, uncommitted, merge, history, commit dialog fit.
import type { Page } from '@playwright/test';
import { test, expect } from './fixtures';
import { login, shot } from './helpers';

const noHScroll = async (page: Page) => expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(1);

test('branch at 390 px: overview, uncommitted, commit dialog, merge, history', async ({ page }) => {
  await login(page, '/_opm/app/app/branch');
  await expect(page.getByTestId('cloud-card')).toBeVisible();
  await noHScroll(page);
  await shot(page, 'br-overview-390');
  await page.getByTestId('branch-tabs-uncommitted').click();
  await expect(page.getByTestId('u-row-sites/app/containers/adminer')).toBeVisible();
  await noHScroll(page);
  await shot(page, 'br-uncommitted-390');
  await page.getByTestId('commit-selected').click();
  const dlg = page.getByTestId('commit-dialog');
  await dlg.getByTestId('commit-message').fill('Record server changes');
  const box = await dlg.getByTestId('commit-submit').boundingBox();
  expect(box!.x + box!.width).toBeLessThanOrEqual(390);
  await shot(page, 'br-commit-dialog-390', false);
  await dlg.getByTestId('commit-submit').click();
  await page.getByTestId('note-discard-file:api-config:sha256:9a1f').click();
  await page.getByTestId('discard-confirm').getByTestId('confirm-ok').click();
  await expect(page.getByTestId('uncommitted-list')).toContainText('Nothing uncommitted');
  await page.getByTestId('branch-tabs-overview').click();
  await page.getByTestId('merge-open').click();
  await expect(page.getByTestId('merge-view').getByTestId('merge-group-removed')).toBeVisible();
  await noHScroll(page);
  await shot(page, 'br-merge-390');
  await page.getByTestId('merge-back').click();
  await page.getByTestId('branch-tabs-history').click();
  await page.getByTestId('history-open-8').click();
  await noHScroll(page);
  await shot(page, 'br-history-390');
});
