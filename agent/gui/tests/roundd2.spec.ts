import { test, expect } from './fixtures';
import { login, open, shot, pickOption } from './helpers';

// Round D part 2: ONE copy per site for files, secrets and volumes; storage locations; agentHome.

test('files: "On the server" column, location in the drawer, editable definition (§21 #113), preview for site', async ({ page }) => {
  await login(page, '/_opm/app/files');
  await expect(page.getByTestId('location-banner')).toHaveText('auto · /var/lib/opm/files/banner');
  await expect(page.getByTestId('location-api-config')).toHaveText('./config/api.json');
  await expect(page.getByTestId('location-api-config')).toHaveAttribute('title', /\/var\/lib\/opm\/config\/api\.json/);
  await shot(page, 'd-files-page');
  // §21 #102: the file definition = the cloud's form; §21 #113: editable on the site (the slug stays fixed)
  await page.getByTestId('details-api-config').click();
  const def = page.getByTestId('file-def-drawer');
  await expect(def.getByTestId('file-label')).toBeEnabled();
  await expect(def.getByTestId('file-location-mode-path')).toHaveAttribute('aria-checked', 'true');
  await expect(def.getByTestId('file-location-path')).toHaveValue('./config/api.json');
  await expect(def.getByTestId('field-lock').first()).toHaveAttribute('title', /Fixed after create/);
  await expect(def.getByTestId('file-def-save')).toBeVisible();
  await shot(page, 'gc-agent-file-details', false);
  await page.keyboard.press('Escape');
  await page.getByTestId('edit-api-config').click();
  const dr = page.getByTestId('file-drawer');
  await expect(dr.getByTestId('file-location')).toContainText('./config/api.json');
  await expect(dr.getByTestId('file-location')).toContainText('app/hello-api, app/hello-api-2');
  await expect(dr).toContainText('once per site');
  await expect(dr.getByTestId('preview-site')).toHaveAttribute('data-value', 'app');
  await dr.getByTestId('preview-site').click();
  await expect(page.getByTestId('preview-site-opt-db')).toContainText('not mounted there');
  await page.keyboard.press('Escape');
  await page.getByTestId('preview-as').click();
  await expect(page.getByTestId('file-render-preview')).toContainText('"site": "app"');
  await pickOption(page, 'preview-site', 'db');
  await expect(page.getByTestId('file-render-preview')).toContainText('"site": "db"');
  await expect(page.getByTestId('file-render-preview')).not.toContainText('Zx8Kq2');
  await shot(page, 'd-files-preview-site', false);
});

test('container: "Inside the container", file rows as text + Edit path, shared copy hint, server locations, summary', async ({ page }) => {
  await login(page, '/_opm/app/app/config/containers/hello-api');
  await expect(page.getByTestId('volumes-table')).toContainText('Inside the container');
  // the file extra mount: text, no required-looking field until "Edit path"
  await expect(page.getByTestId('extra-row-2')).toContainText('mounted read-only at /app/config.json');
  await expect(page.getByTestId('extra-2-path')).toHaveText('/app/config.json');
  await expect(page.getByTestId('extra-2-mount')).toHaveCount(0);
  // round T: compact badge + popover list (was the long "shared copy · also used by …" text)
  await expect(page.getByTestId('extra-2-shared-copy')).toHaveText('shared · 2 containers');
  await expect(page.getByTestId('extra-2-shared-copy-list')).toContainText('hello-api-2');
  await expect(page.getByTestId('extra-2-server')).toContainText('./config/api.json');
  await expect(page.getByTestId('vol-data-server')).toContainText('auto · /var/lib/opm/volumes/hello-api-data');
  await expect(page.getByTestId('sum-mounts')).toContainText('/app/config.json ← file api-config · ro');
  await expect(page.getByTestId('sum-mounts')).toContainText('server: ./config/api.json');
  await page.getByTestId('volumes-table').scrollIntoViewIfNeeded();
  await shot(page, 'd-files-container');
  await page.getByTestId('extra-2-edit-path').click();
  await expect(page.getByTestId('extra-2-mount-input')).toHaveValue('/app/config.json');
  // an image volume pointed at a secret: text + "shared copy" when another container mounts the same secret
  await pickOption(page, 'vol-env-config-source', 'secret:api-token');
  await expect(page.getByTestId('vol-row-env-config')).toContainText('mounted read-only at /app/helper/config/env/worker.ts');
  await expect(page.getByTestId('vol-env-config-server')).toContainText('/var/lib/opm/secrets/api-token');
  await open(page, '/_opm/app/app/config/containers/hello-api-2');
  await expect(page.getByTestId('extra-2-shared-copy')).toHaveText('shared · 2 containers');
  await expect(page.getByTestId('extra-2-shared-copy-list')).toContainText('hello-api');
  await expect(page.getByTestId('vol-data-server')).toContainText('./cache/hello-api-2');
});

test('site volumes: "On the server" column, Auto / Custom path in the modal (V_LOCATION), Backups shows the absolute path', async ({ page }) => {
  await login(page, '/_opm/app/app/config/volumes');
  await expect(page.getByTestId('vloc-api-env')).toHaveText('auto · /var/lib/opm/volumes/api-env');
  await expect(page.getByTestId('vloc-hello-api-2-data')).toHaveText('./cache/hello-api-2');
  await page.getByTestId('add-volume').click();
  const m = page.getByTestId('volume-modal');
  await m.getByTestId('vm-slug').fill('cassandra-data');
  await expect(m.getByTestId('vm-location-hint')).toContainText('/var/lib/opm/volumes/cassandra-data');
  await m.getByTestId('vm-location-mode').getByRole('radio', { name: 'Custom path' }).click();
  await m.getByTestId('vm-location-path').fill('./snapshots/x');
  await expect(m.getByTestId('vm-location-err')).toContainText('belongs to the agent');
  await m.getByTestId('vm-location-path').fill('./data/../x');
  await expect(m.getByTestId('vm-location-err')).toContainText('".."');
  await m.getByTestId('vm-location-path').fill('./data/cassandra');
  await expect(m.getByTestId('vm-location-err')).toHaveCount(0);
  await expect(m.getByTestId('vm-location-hint')).toContainText('/var/lib/opm/data/cassandra');
  await shot(page, 'd-volume-location', false);
  await m.getByTestId('vm-save').click();
  await expect(page.getByTestId('vloc-cassandra-data')).toHaveText('./data/cassandra');
  // switch back to auto via Edit
  await page.getByTestId('edit-cassandra-data').click();
  await expect(m.getByTestId('vm-location-path')).toHaveValue('./data/cassandra');
  await m.getByTestId('vm-location-mode').getByRole('radio', { name: 'Auto (recommended)' }).click();
  await m.getByTestId('vm-save').click();
  await expect(page.getByTestId('vloc-cassandra-data')).toHaveText('auto · /var/lib/opm/volumes/cassandra-data');

  await open(page, '/_opm/app/backups');
  await expect(page.getByTestId('site-volumes-app')).toContainText('agent home /var/lib/opm');
  await expect(page.getByTestId('svpath-app-hello-api-2-data')).toContainText('/var/lib/opm/cache/hello-api-2');
  await expect(page.getByTestId('svpath-app-hello-api-2-data')).toContainText('custom');
  await expect(page.getByTestId('svpath-db-redis-data')).toContainText('/opt/opm-db/volumes/redis-data');
});
