// §21 #110 an update never freezes anything: the lock is advisory (a warning bar), nothing is disabled while an update
// runs, and the bar stops it on every site — just stop · stop & roll back · stop, restart agents & roll back.
import type { Page } from '@playwright/test';
import { test, expect } from './fixtures';
import { knobs, login, nav, pickOption, shot } from './helpers';

const SHOTS = process.env.OPM_S110_SHOTS === '1';

/** commit one variable change → an update runs (mock: ~10 s) → the warning bar shows */
async function startUpdate(page: Page) {
  await knobs(page, { 'opm.mock.branch.clean': '1', 'opm.mock.restartSec': '2' });
  await login(page, '/_opm/app/app/variables');
  await pickOption(page, 'var-theme', 'blue');
  await page.getByTestId('change-bar-commit').click();
  const dlg = page.getByTestId('local-commit-dialog');
  await dlg.getByTestId('commit-message').fill('Theme blue');
  await dlg.getByTestId('commit-submit').click();
  await expect(page).toHaveURL(/\/branch$/);
  const bar = page.getByTestId('lock-banner');
  await expect(bar).toBeVisible({ timeout: 15_000 });
  return bar;
}
async function confirmStop(page: Page, button: string, text: RegExp) {
  await page.getByTestId(button).click();
  const dlg = page.getByTestId('lock-confirm');
  await expect(dlg).toContainText(text);
  await dlg.getByTestId('confirm-ok').click();
  await expect(dlg).toBeHidden();
}

test('a running update is a warning: nothing is disabled; Stop update stops it on every site (nothing rolled back)', async ({ page }) => {
  const bar = await startUpdate(page);
  await expect(bar).toHaveAttribute('data-kind', /^(update|merge)$/);
  await expect(bar).toContainText('An update is running on all sites (coordinator app');
  await expect(bar).toContainText('You can keep working — starting another update stops this one.');
  await expect(bar).not.toContainText('locked');
  for (const b of ['lock-stop', 'lock-stop-rollback', 'lock-stop-restart']) await expect(bar.getByTestId(b)).toBeEnabled();
  await expect(bar.getByTestId('lock-view')).toHaveAttribute('href', /\/branch$/);
  if (SHOTS) await shot(page, 's110-lock-bar-light', false);

  // other pages keep working while it runs
  await nav(page, 'backups');
  await expect(page.getByTestId('take-snapshot')).toBeEnabled();
  await expect(page.getByTestId('lock-banner')).toBeVisible();

  await confirmStop(page, 'lock-stop', /Nothing is rolled back: the containers stay as they are/);
  await expect(page.getByTestId('lock-results')).toContainText('app');
  await expect(page.getByTestId('lock-results')).toContainText('stopped');
  await expect(page.getByTestId('lock-banner')).toHaveCount(0, { timeout: 10_000 });
});

test('Stop & roll back → a rollback runs (itself stoppable)', async ({ page }) => {
  await startUpdate(page);
  await confirmStop(page, 'lock-stop-rollback', /rolls back to its pre-update backup/);
  await expect(page.getByTestId('lock-results')).toContainText('rolling back to the pre-update backup');
  const bar = page.getByTestId('lock-banner');
  await expect(bar).toHaveAttribute('data-kind', 'rollback', { timeout: 10_000 });
  await expect(bar).toContainText('A rollback is running on all sites');
  await expect(bar.getByTestId('lock-stop-rollback')).toHaveCount(0);
  if (SHOTS) await shot(page, 's110-rollback-bar-light', false);
  await confirmStop(page, 'lock-stop', /Stops the rollback on every site/);
  await expect(page.getByTestId('lock-banner')).toHaveCount(0, { timeout: 10_000 });
});

test('Stop, restart agents & roll back → the agents restart, the page reconnects on the same session', async ({ page }) => {
  await startUpdate(page);
  await confirmStop(page, 'lock-stop-restart', /Restarts every agent/);
  await expect(page.getByTestId('lock-results')).toContainText('the agent restarts, then rolls back');
  await expect(page.getByTestId('restart-overlay')).toBeVisible();
  await expect(page.getByTestId('restart-overlay')).toBeHidden({ timeout: 30_000 });
  await expect(page.getByText('The agent is back')).toBeVisible();
  await expect(page.getByTestId('actor-chip')).toBeVisible();
});
