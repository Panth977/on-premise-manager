// SPEC-final §21 #101 / #117 — persist-to-cloud volumes in Docker › Volumes (mock agent, lib/mock/volsync.ts): the
// state badge compares the live folder with the committed content (in sync / changed on the server), the Cloud sync
// tab links to the Branch page (no Snapshot now / Apply cloud version), and Config › Volumes content / persist READ-ONLY.
import { test, expect } from './fixtures';
import { knobs, login, open, shot } from './helpers';

const V = (site: string, q = '') => `/_opm/app/${site}/volumes${q}`;

test.beforeEach(async ({ page }) => {
  await knobs(page, { 'opm.mock.waitSec': '2' });
});

test('sync state badges: live vs committed on the volume list + the Cloud sync tab (Branch page, Rescan, no Apply)', async ({ page }) => {
  await login(page, V('app'));
  await expect(page.getByTestId('sync-badge-hello-api-data')).toHaveText('Matches the commit');
  await page.getByTestId('vol-hello-api-data').click();
  await page.getByTestId('ov-sync-open').click();
  const sp = page.getByTestId('vol-sync');
  await expect(sp).toHaveAttribute('data-state', 'in-sync');
  await expect(sp.getByTestId('sync-committed')).toBeVisible();
  await expect(sp.getByTestId('sync-live')).toBeVisible();
  await expect(sp.getByTestId('sync-snapshot')).toHaveCount(0);
  await expect(sp.getByTestId('sync-apply')).toHaveCount(0);
  // §21 #102: the shared persist form, read-only (patterns are disabled inputs)
  await expect(sp.getByTestId('sync-persist').locator('[data-testid^="persist-exclude-"]').first()).toBeDisabled();
  await open(page, V('db', '?v=redis-data&tab=sync'));
  await expect(page.getByTestId('sync-badge-redis-data')).toHaveText('Error');
  await expect(page.getByTestId('sync-error')).toContainText('over the cap');
  await open(page, V('northwind'));
  await expect(page.getByTestId('sync-badge-ez-scripts')).toHaveText('Changed on the server');
  // a non-persist volume has no badge and no Cloud sync tab
  await expect(page.getByTestId('sync-badge-admin-runtime')).toHaveCount(0);
  await page.getByTestId('vol-ez-scripts').click();
  await page.getByTestId('vtab-sync').click();
  await expect(page.getByTestId('sync-uncommitted')).toContainText('not committed');
  await expect(page.getByTestId('sync-console')).toHaveAttribute('href', /\/sites\/northwind\/volumes\/content\?volume=ez-scripts$/);
  await shot(page, 'vc-volumes-sync-light');
  await page.emulateMedia({ colorScheme: 'dark' });
  await shot(page, 'vc-volumes-sync-dark');
  await page.getByTestId('sync-branch').click();
  await expect(page).toHaveURL(/\/_opm\/app\/northwind\/branch/);
});

test('Config › Volumes: starter content + persist read-only, legacy File seed, no "From files" UI', async ({ page }) => {
  await login(page, '/_opm/app/northwind/config/volumes');
  await expect(page.getByTestId('vseed-text-nodered-data')).toContainText('2 files');
  await expect(page.getByTestId('vpersist-nodered-data')).toBeVisible();
  await expect(page.getByTestId('vseed-text-admin-runtime')).toContainText('1 file');
  await page.getByTestId('edit-nodered-data').click();
  const d = page.getByTestId('volume-modal');
  await expect(d.getByTestId('vm-seed-view-tree')).toContainText('settings.js');
  await expect(d.getByTestId('vm-seed-view-tree')).toContainText('template (once per site)');
  await expect(d.getByTestId('vm-seed-view-tree')).toContainText('lib/');
  await expect(d.getByTestId('vm-persist')).toContainText('node_modules/');
  await expect(d.getByTestId('vm-console-link')).toHaveAttribute('href', /\/app\/deployments\/.+\/sites\/northwind\/volumes\/content\?volume=nodered-data$/);
  await expect(d.getByTestId('vm-seed-mode-files')).toHaveCount(0);
  await expect(d.getByTestId('seed-add')).toHaveCount(0);
  await shot(page, 'vc-config-volume-readonly', false);
  // saving a label keeps content + persist (no V_LOCAL_VOLUME_CONTENT)
  await d.getByTestId('vm-label').fill('Node-RED data (flows)');
  await d.getByTestId('vm-save').click();
  await expect(page.getByTestId('volume-row-nodered-data')).toContainText('edited');
  await expect(page.getByText('V_LOCAL_VOLUME_CONTENT')).toHaveCount(0);
  await open(page, '/_opm/app/app/config/volumes');
  await expect(page.getByTestId('vseed-text-api-env')).toHaveText('(legacy) from Files api-config');
  await page.getByTestId('edit-api-env').click();
  await expect(page.getByTestId('volume-modal').getByTestId('vm-seed-view')).toContainText('worker.ts ← file api-config');
  // a new volume starts empty (content is cloud configuration)
  await page.keyboard.press('Escape');
  await page.getByTestId('add-volume').click();
  await expect(page.getByTestId('volume-modal').getByTestId('vm-seed-own')).toContainText('starts empty');
});
