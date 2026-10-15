// §21 #104 evidence screenshots (light + dark, desktop): Branch overview, Uncommitted, Merge (with a removal), History,
// Commit dialog → screenshots/br-*-{light,dark}.png. The 390 px ones: branch-mobile.spec.ts.
import type { Page } from '@playwright/test';
import { test, expect } from './fixtures';
import { knobs, login, open, shot } from './helpers';

async function all(page: Page, scheme: 'light' | 'dark') {
  await page.emulateMedia({ colorScheme: scheme });
  await login(page, '/_opm/app/app/branch');
  await expect(page.getByTestId('cloud-card')).toContainText('seq 9');
  await shot(page, `br-overview-${scheme}`);
  await page.getByTestId('branch-tabs-uncommitted').click();
  await expect(page.getByTestId('u-row-sites/app/containers/adminer')).toBeVisible();
  await shot(page, `br-uncommitted-${scheme}`);
  await page.getByTestId('commit-selected').click();
  await page.getByTestId('commit-dialog').getByTestId('commit-message').fill('Record adminer added by hand on the server');
  await shot(page, `br-commit-dialog-${scheme}`, false);
  await page.getByTestId('commit-dialog').getByTestId('commit-submit').click();
  await expect(page.getByTestId('uncommitted-list')).toContainText('Notes');
  // discard the note so the site is clean, then merge
  await page.getByTestId('note-discard-file:api-config:sha256:9a1f').click();
  await page.getByTestId('discard-confirm').getByTestId('confirm-ok').click();
  await expect(page.getByTestId('uncommitted-list')).toContainText('Nothing uncommitted');
  await page.getByTestId('branch-tabs-overview').click();
  await page.getByTestId('merge-open').click();
  const view = page.getByTestId('merge-view');
  await expect(view.getByTestId('merge-group-removed')).toBeVisible();
  await view.getByTestId('merge-take-variables/theme/value').check();
  await view.getByTestId('merge-take-sites/app/containers/adminer').check();
  await view.getByTestId('merge-expand-variables/theme/value').click();
  await shot(page, `br-merge-${scheme}`);
  await page.goto('about:blank');
}

test('screens · light', async ({ page }) => all(page, 'light'));
test('screens · dark', async ({ page }) => all(page, 'dark'));
test('screens · history (light + dark)', async ({ page }) => {
  await knobs(page, { 'opm.mock.branch.clean': '1' });
  await login(page, '/_opm/app/app/branch?tab=history');
  for (const scheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme: scheme });
    if (scheme === 'dark') await open(page, '/_opm/app/app/branch?tab=history');
    await page.getByTestId('history-open-7').click();
    await expect(page.getByTestId('history-detail')).toBeVisible();
    await shot(page, `br-history-${scheme}`);
  }
});
