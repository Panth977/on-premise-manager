import { test, expect } from './fixtures';
import { knobs, login, open, shot, pickOption, nav } from './helpers';

// §21 #104 / #105 Branch page: cloud head vs this site, uncommitted (= drift + notes only; edits live in the browser), commit, merge (cherry-pick +
// "remove locally"), fast-forward all, history / revert. The mock: cloud 2 commits ahead; a container created by hand
// (adminer), an edited per-site file and a note (an edited rendered file).
const SHOTS = process.env.OPM_BR_SHOTS === '1';
const snap = async (page: Parameters<typeof shot>[0], name: string) => {
  if (SHOTS) await shot(page, name);
};

test('overview: cloud vs this site, uncommitted counts, merge blocked while dirty', async ({ page }) => {
  await login(page, '/_opm/app/app/branch');
  const cloud = page.getByTestId('cloud-card');
  await expect(cloud).toContainText('seq 9');
  await expect(cloud).toContainText('Greeting from the cloud');
  await expect(page.getByTestId('behind')).toHaveText('2 to merge');
  await expect(page.getByTestId('local-card')).toContainText('seq 7');
  await expect(page.getByTestId('merged-from')).toHaveText('seq 7');
  await expect(page.getByTestId('n-edits')).toHaveText('0');
  await expect(page.getByTestId('n-drift')).toHaveText('2');
  await expect(page.getByTestId('n-notes')).toHaveText('1');
  await expect(page.getByTestId('merge-blocked')).toContainText('Commit or discard the 3 changes made on the server first');
  await expect(page.getByTestId('tree-app-branch')).toBeVisible();
  await expect(page.getByTestId('branch-badge')).toHaveText('5');
  await snap(page, 'br-overview-light');
  // the merge page says the same and cannot merge
  await open(page, '/_opm/app/app/branch/merge');
  await expect(page.getByTestId('merge-dirty-banner')).toBeVisible();
  await expect(page.getByTestId('merge-submit')).toBeDisabled();
});

test('old Diff / Updates URLs land on Branch', async ({ page }) => {
  await login(page, '/_opm/app/app/diff?rev=x');
  await expect(page).toHaveURL(/\/_opm\/app\/app\/branch$/);
  await open(page, '/_opm/app/updates');
  await expect(page).toHaveURL(/\/branch$/);
});

test('edit → ONE commit from the change bar (no Save) → applied → history', async ({ page }) => {
  await knobs(page, { 'opm.mock.branch.clean': '1' });
  await login(page, '/_opm/app/app/variables');
  await pickOption(page, 'var-theme', 'blue');
  const bar = page.getByTestId('local-edit-bar');
  await expect(bar).toContainText('1 change (not committed)');
  await expect(page.getByTestId('save-working')).toHaveCount(0);
  // (the mock agent lives in the page: navigate inside the app) — nothing is on the agent yet
  await nav(page, 'branch');
  await expect(page.getByTestId('n-edits')).toHaveText('1');
  await page.getByTestId('branch-tabs-uncommitted').click();
  await expect(page.getByTestId('uncommitted-list')).toContainText('Nothing uncommitted');
  await page.getByTestId('tree-app-variables').click();
  await page.getByTestId('change-bar-commit').click();
  const dlg = page.getByTestId('local-commit-dialog');
  await expect(dlg.getByTestId('commit-submit')).toBeDisabled();
  await dlg.getByTestId('commit-message').fill('Theme blue for the control room');
  await snap(page, 'br-commit-dialog-light');
  await dlg.getByTestId('commit-submit').click();
  await expect(page).toHaveURL(/\/branch$/);
  await expect(page.getByTestId('branch-run')).toBeVisible();
  await expect(page.getByTestId('update-result')).toContainText('Applied', { timeout: 40_000 });
  // §21 #127 only the containers that change are listed; the rest keep running (one badge)
  await expect(page.getByTestId('progress-kept-app')).toContainText('unchanged · kept running');
  await page.getByTestId('branch-tabs-history').click();
  await expect(page.getByTestId('history-row-8')).toContainText('Theme blue for the control room', { timeout: 10_000 });
  await page.getByTestId('branch-tabs-overview').click();
  await expect(page.getByTestId('n-edits')).toHaveText('0');
});

test('commit the server changes (+ accept the note) → merge: cherry-pick one change + remove locally → applied', async ({ page }) => {
  await login(page, '/_opm/app/app/branch?tab=uncommitted');
  await expect(page.getByTestId('u-row-sites/app/containers/adminer')).toHaveAttribute('data-origin', 'docker');
  await expect(page.getByTestId('u-row-sites/app/containers/adminer')).toContainText('created by hand');
  await page.getByTestId('note-accept-file:api-config:sha256:9a1f').check();
  await snap(page, 'br-uncommitted-light');
  await page.getByTestId('commit-selected').click();
  const dlg = page.getByTestId('commit-dialog');
  await expect(dlg.getByTestId('commit-submit')).not.toContainText('apply');
  await dlg.getByTestId('commit-message').fill('Record adminer + the edited cert added by hand on the server');
  await dlg.getByTestId('commit-submit').click();
  await expect(page.getByTestId('uncommitted-list')).toContainText('Nothing uncommitted', { timeout: 10_000 });

  await page.getByTestId('branch-tabs-overview').click();
  await expect(page.getByTestId('merge-blocked')).toHaveCount(0);
  await page.getByTestId('merge-open').click();
  await expect(page).toHaveURL(/\/branch\/merge\?source=/);
  const view = page.getByTestId('merge-view');
  await expect(view.getByTestId('merge-item-variables/theme/value')).toBeVisible();
  // adminer is on this site only → offered as "remove locally"
  const rm = view.getByTestId('merge-item-sites/app/containers/adminer');
  await expect(rm).toHaveAttribute('data-effect', 'remove');
  await expect(view.getByTestId('merge-group-removed')).toContainText(/remove/i);
  // the file edited on the server: a text diff from GET branch/blobs/{sha256}
  await view.getByTestId('merge-expand-files/client-cert').click();
  await expect(view.getByTestId('merge-textdiff-files/client-cert')).toContainText('renewed by hand on the server');
  await view.getByTestId('merge-take-variables/theme/value').check();
  await view.getByTestId('merge-take-sites/app/containers/adminer').check();
  await snap(page, 'br-merge-light');
  await expect(view.getByTestId('merge-submit')).toBeEnabled({ timeout: 10_000 });
  await view.getByTestId('merge-submit').click();
  const cd = page.getByTestId('merge-commit-dialog');
  await cd.getByTestId('commit-message').fill('Take the yellow theme; drop adminer');
  await cd.getByTestId('commit-submit').click();
  await expect(page).toHaveURL(/\/branch$/);
  await expect(page.getByTestId('update-result')).toContainText('Applied', { timeout: 40_000 });
  await page.getByTestId('branch-tabs-history').click();
  const row = page.getByTestId('history-row-9');
  await expect(row).toContainText('Take the yellow theme; drop adminer', { timeout: 10_000 });
  await expect(row).toContainText(/cherry-pick/i);
  // still behind: the greeting / memory / route changes were not taken
  await page.getByTestId('branch-tabs-overview').click();
  await expect(page.getByTestId('merged-from')).toHaveText('seq 9');
});

test('fast-forward all sites (clean, strictly behind)', async ({ page }) => {
  await knobs(page, { 'opm.mock.branch.clean': '1' });
  await login(page, '/_opm/app/app/branch');
  await expect(page.getByTestId('ff-sites')).toBeVisible();
  await expect(page.getByTestId('ff-pick-northwind')).toBeDisabled();
  await expect(page.getByTestId('ff-pick-db')).toBeEnabled();
  await page.getByTestId('ff-open').click();
  const dlg = page.getByTestId('ff-dialog');
  await expect(dlg).toContainText('app, db');
  await dlg.getByTestId('commit-message').fill('Release 9 everywhere');
  await dlg.getByTestId('commit-submit').click();
  await expect(page.getByTestId('update-result')).toContainText('Applied', { timeout: 40_000 });
});

test('history: open, compare two, revert a commit (applied)', async ({ page }) => {
  await knobs(page, { 'opm.mock.branch.clean': '1' });
  await login(page, '/_opm/app/app/branch?tab=history');
  await expect(page.getByTestId('history-row-7')).toBeVisible();
  await page.getByTestId('history-pick-7').check();
  await page.getByTestId('history-pick-5').uncheck().catch(() => {});
  await page.getByTestId('history-pick-7').uncheck();
  await page.getByTestId('history-pick-6').check();
  await page.getByTestId('history-pick-5').check();
  await page.getByTestId('history-compare').click();
  await expect(page.getByTestId('history-compare-result')).toContainText('greeting', { ignoreCase: true });
  await page.getByTestId('history-open-6').click();
  await expect(page.getByTestId('history-detail')).toBeVisible();
  await snap(page, 'br-history-light');
  await page.getByTestId('history-detail').getByTestId('revert').click();
  const dlg = page.getByTestId('history-commit-dialog');
  await dlg.getByTestId('commit-message').fill('Revert the greeting change');
  await dlg.getByTestId('commit-submit').click();
  await expect(page.getByTestId('branch-run')).toBeVisible({ timeout: 10_000 });
  await expect(page.getByTestId('update-result')).toContainText('Applied', { timeout: 40_000 });
});
