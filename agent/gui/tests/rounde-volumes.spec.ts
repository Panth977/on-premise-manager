import { test, expect } from './fixtures';
import { knobs, login, shot, pickOption } from './helpers';

/** Round E addition (B): volume kind (folder / single file) + starter content + Reset to starter content. */

test('volume drawer: single file kind (rename warning); starter content is cloud-only (§21 #101: read-only, new volumes start empty)', async ({ page }) => {
  await login(page, '/_opm/app/app/config/volumes');
  await expect(page.getByTestId('vseed-api-env')).toHaveText(/\(legacy\) from Files api-config/);
  await page.getByTestId('add-volume').click();
  const d = page.getByTestId('volume-modal');
  await d.getByTestId('vm-slug').fill('app-settings');
  await d.getByTestId('vm-kind').getByRole('radio', { name: 'Single file' }).click();
  await expect(d.getByTestId('vm-file-warning')).toContainText('Node-RED');
  // §21 #101: no "From files" / "File" starter content — Files are never volume content
  await expect(d.getByTestId('vm-seed-mode-files')).toHaveCount(0);
  await expect(d.getByTestId('vm-seed-own')).toContainText('starts empty');
  await shot(page, 'e-volumes-drawer-file');
  await d.getByTestId('vm-save').click();
  await expect(page.getByTestId('vkind-app-settings')).toHaveText('single file');
  await expect(page.getByTestId('vseed-app-settings')).toContainText('Empty');
  await shot(page, 'e-volumes-table');

  // a single-file volume on an image FOLDER path is flagged in the container editor
  await page.getByTestId('nav-containers').click();
  await page.getByTestId('containers').getByRole('link', { name: 'hello-api', exact: true }).click();
  await page.waitForURL(/containers\/hello-api$/);
  const src = page.getByTestId('vol-data-source');
  await src.click();
  await expect(page.getByTestId('vol-data-source-list').getByRole('option').filter({ hasText: 'single file' })).toHaveCount(1);
  await page.getByTestId('vol-data-source-opt-volume:app-settings').click();
  // shared container form (§21 #102): the validator's kind issue, with the same fixes as the cloud
  await expect(page.locator('[data-testid="vol-data-issue"][data-code="V_VOLUME_KIND_IMAGE"]')).toBeVisible();
  // §21 #83: the image volume kind is shown per row; an explicit Folder wins over the ".ts" look of the path
  await expect(page.getByTestId('vol-data-imgkind')).toHaveText('Folder');
  await expect(page.getByTestId('vol-env-config-imgkind')).toHaveText('Folder');
  await expect(page.locator('[data-testid="vol-env-config-issue"][data-code="V_VOLUME_KIND_MISMATCH"]')).toHaveCount(0);
  await shot(page, 'e-volumes-container-flag');
  // the review lists the new volume
  await page.getByTestId('review-changes').click();
  const diff = page.getByTestId('review-drawer').getByTestId('diff');
  await diff.getByRole('tab', { name: /volumes/ }).click();
  await expect(diff).toContainText('app-settings');
  await shot(page, 'e-volumes-review');
});

test('Backups: kind / starts with / seeded, Reset to starter content (wait + ACCEPT, snapshot first)', async ({ page }) => {
  await knobs(page, { 'opm.mock.waitSec': '2' });
  await login(page, '/_opm/app/backups');
  const card = page.getByTestId('site-volumes-app');
  await expect(card.getByTestId('svseed-app-api-env')).toContainText('(legacy) from Files api-config');
  await expect(card.getByTestId('svseed-app-api-env')).toContainText('seeded');
  await expect(page.getByTestId('site-volumes-northwind').getByTestId('svseed-northwind-admin-runtime')).toContainText('1 file');
  // peers: reset only from that site's own agent GUI
  await expect(page.getByTestId('reset-northwind-nodered-data')).toBeDisabled();
  await card.getByTestId('reset-app-api-env').click();
  const flow = page.getByTestId('reset-flow');
  await expect(flow.getByTestId('countdown')).toBeVisible();
  await shot(page, 'e-volumes-reset-wait', false);
  await flow.getByTestId('accept-input').fill('ACCEPT');
  await shot(page, 'e-volumes-reset-accept', false);
  await flow.getByTestId('start-reset').click();
  await expect(card.getByTestId('reset-done-app')).toContainText('1 entry written');
  await shot(page, 'e-volumes-backups');
});
