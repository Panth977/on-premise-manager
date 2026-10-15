import { test, expect } from './fixtures';
import { expectOnTop, knobs, login, open, shot, pickOption, nav } from './helpers';

// Round D: declared site volumes, one grouped source dropdown, Backups per-site volumes, site sections in the sidebar.

test('site Volumes page: table, add / edit / backup toggle, delete blocked when used, delete unused, review diff', async ({ page }) => {
  await login(page, '/_opm/app/app/config/volumes');
  const t = page.getByTestId('volumes-table');
  await expect(page.getByTestId('volumes-info')).toContainText('Host paths are never backed up');
  await expect(page.getByTestId('volume-row-api-env')).toContainText('API env config');
  await expect(page.getByTestId('used-api-env-hello-api')).toBeVisible();
  await expect(page.getByTestId('used-api-env-hello-api-2')).toBeVisible();
  await expect(page.getByTestId('backup-hello-api-2-data')).not.toBeChecked();
  await expect(page.getByTestId('backup-hello-api-data')).toBeChecked();
  await shot(page, 'd-volumes-page');

  // add
  await page.getByTestId('add-volume').click();
  const m = page.getByTestId('volume-modal');
  await expectOnTop(page, '[data-testid="volume-modal"] .inner');
  await m.getByTestId('vm-slug').fill('Bad Name!');
  await expect(m).toContainText('starting with a letter or digit');
  await expect(m.getByTestId('vm-save')).toBeDisabled();
  await m.getByTestId('vm-slug').fill('api-env');
  await expect(m).toContainText('already has a volume api-env');
  await m.getByTestId('vm-slug').fill('cassandra-data');
  await m.getByTestId('vm-label').fill('Cassandra data');
  await m.getByTestId('vm-desc').fill('SSTables + commit log');
  await shot(page, 'd-volume-modal', false);
  await m.getByTestId('vm-save').click();
  await expect(m).toBeHidden();
  const row = page.getByTestId('volume-row-cassandra-data');
  await expect(row).toContainText('new');
  await expect(row).toContainText('unused');
  await expect(page.getByTestId('issues')).toContainText('V_VOLUME_UNUSED');
  // edit: the name is immutable
  await page.getByTestId('edit-cassandra-data').click();
  await expect(m.getByTestId('vm-slug')).toBeDisabled();
  await m.getByTestId('vm-label').fill('Cassandra');
  await m.getByTestId('vm-save').click();
  await expect(row).toContainText('Cassandra');
  // backup toggle
  await page.getByTestId('backup-hello-api-2-data').check();
  await expect(page.getByTestId('volume-row-hello-api-2-data')).toContainText('edited');
  await expect(page.getByTestId('local-edit-bar')).toContainText('2 changes (not committed)');

  // delete a used volume → blocked with the list of containers
  await page.getByTestId('delete-api-env').click();
  const b = page.getByTestId('delete-blocked');
  await expect(b).toBeVisible();
  await expect(b).toContainText('hello-api');
  await expect(b).toContainText('hello-api-2');
  await shot(page, 'd-delete-blocked', false);
  await b.getByRole('button', { name: 'OK' }).click();
  await expect(page.getByTestId('volume-row-api-env')).toBeVisible();
  // delete an unused one
  await page.getByTestId('delete-cassandra-data').click();
  await page.getByTestId('delete-confirm').getByRole('button', { name: 'Remove' }).click();
  await expect(t.getByTestId('volume-row-cassandra-data')).toHaveCount(0);
  await expect(page.getByTestId('local-edit-bar')).toContainText('1 change (not committed)');

  // review: the diff has a volumes section
  await page.getByTestId('review-changes').click();
  const diff = page.getByTestId('review-drawer').getByTestId('diff');
  await expect(diff.getByRole('tab', { name: /volumes/ })).toHaveAttribute('aria-selected', 'true');
  await expect(diff).toContainText('app/volumes/hello-api-2-data');
  await shot(page, 'd-volumes-review', false);
});

test('container Volumes: one grouped dropdown, read-write default, read-only fix, + New data volume, path edit/reset', async ({ page }) => {
  await login(page, '/_opm/app/app/config/containers/hello-api');
  const src = page.getByTestId('vol-data-source');
  await expect(src).toHaveAttribute('data-value', 'volume:hello-api-data');
  // groups: Data volumes (with "shared with"), Files, Secrets, Advanced (custom Select, round T)
  await src.click();
  const list = page.getByTestId('vol-data-source-list');
  await expect(list.getByRole('group')).toHaveCount(4);
  await expect(list.getByRole('group', { name: 'Data volumes' }).getByRole('option')).toHaveCount(4); // 3 declared + "+ New data volume…"
  await expect(list.getByTestId('vol-data-source-opt-none')).toContainText('Not mounted');
  await expect(list.getByTestId('vol-data-source-opt-host')).toContainText('Host path…');
  await page.keyboard.press('Escape');
  await page.getByTestId('vol-env-config-source').click();
  await expect(page.getByTestId('vol-env-config-source-opt-volume:api-env')).toContainText('shared with hello-api-2');
  await page.keyboard.press('Escape');
  // data volume = read-write by default
  await expect(page.getByTestId('vol-data-ro')).not.toBeChecked();
  await expect(page.getByTestId('vol-row-data')).toContainText('read-write');
  await shot(page, 'd-container-volumes');
  // file → read-only badge, then back to a data volume → read-write again (round-C bug: RO stayed on)
  await pickOption(page, 'vol-data-source', 'file:api-config');
  await expect(page.getByTestId('vol-data-ro')).toHaveCount(0);
  await expect(page.getByTestId('vol-row-data')).toContainText('read-only');
  await pickOption(page, 'vol-data-source', 'volume:api-env');
  await expect(page.getByTestId('vol-data-ro')).not.toBeChecked();
  await expect(page.getByTestId('sum-mounts')).toContainText('/data ← volume api-env');
  await expect(page.getByTestId('sum-mounts')).not.toContainText('/data ← volume api-env · ro');
  // explicit read-only stays when switching between data volumes
  await page.getByTestId('vol-data-ro').check();
  await pickOption(page, 'vol-data-source', 'volume:hello-api-2-data');
  await expect(page.getByTestId('vol-data-ro')).toBeChecked();
  await page.getByTestId('vol-data-ro').uncheck();

  // + New data volume… → the same Add modal, prefilled <container>-<imagevolume> (free name), declares + selects
  await src.click();
  await page.getByTestId('vol-data-source-opt-new-volume').click();
  const m = page.getByTestId('volume-modal');
  await expect(m).toBeVisible();
  await expect(m.getByTestId('vm-slug')).toHaveValue('hello-api-data-2');
  await expect(src).toHaveAttribute('data-value', 'volume:hello-api-2-data'); // unchanged until saved
  await shot(page, 'd-new-volume', false);
  await m.getByTestId('vm-save').click();
  await expect(src).toHaveAttribute('data-value', 'volume:hello-api-data-2');
  await expect(page.getByTestId('vol-data-server')).toContainText('auto · /var/lib/opm/volumes/hello-api-data-2 · backed up');
  await expect(page.getByTestId('vol-data-ro')).not.toBeChecked();

  // mount path: effective image path + Edit path / Reset
  await expect(page.getByTestId('vol-data-path')).toContainText('/data');
  await page.getByTestId('vol-data-edit-path').click();
  await page.getByTestId('vol-data-mount-input').fill('/srv/data');
  await expect(page.getByTestId('sum-mounts')).toContainText('/srv/data ← volume hello-api-data-2');
  await page.getByTestId('vol-data-reset-path').click();
  await expect(page.getByTestId('vol-data-path')).toContainText('/data');

  // Not mounted (explicit) has no warning; host path shows "never backed up"
  await pickOption(page, 'vol-env-config-source', 'host');
  await expect(page.getByTestId('vol-row-env-config')).toContainText('never backed up');
  await pickOption(page, 'vol-env-config-source', 'none');
  await expect(page.getByTestId('vol-row-env-config').getByTestId('vol-env-config-issue')).toHaveCount(0);

  // the new volume is on the site Volumes page (same working copy; sidebar navigation keeps the edits)
  await page.getByTestId('nav-volumes').click();
  await page.waitForURL(/config\/volumes$/);
  await expect(page.getByTestId('volume-row-hello-api-data-2')).toContainText('new');
  await expect(page.getByTestId('used-hello-api-data-2-hello-api')).toBeVisible();
  // hello-api-data is now unused (warning only)
  await expect(page.getByTestId('volume-row-hello-api-data')).toContainText('unused');
});

test('config sections = tabs of one Config page (nav2); no top bar; breadcrumb; old ?tab= links redirect', async ({ page }) => {
  await login(page, '/_opm/app');
  // no top bar in any scope: status strip in the sidebar header, account + theme in the sidebar footer
  await expect(page.locator('header.top')).toHaveCount(0);
  await expect(page.getByTestId('status-strip')).toContainText('head seq 7');
  await expect(page.getByRole('navigation', { name: 'Main' }).getByTestId('actor-chip')).toBeVisible();
  await expect(page.getByRole('navigation', { name: 'Main' }).getByTestId('theme')).toBeVisible();
  await shot(page, 'd-nav-root', false);

  await open(page, '/_opm/app/app/config');
  await page.waitForURL(/\/_opm\/app\/app\/config\/containers$/);
  await expect(page.locator('header.top')).toHaveCount(0);
  const tree = page.getByTestId('config-tabs');
  await expect(tree.getByTestId('nav-containers')).toHaveAttribute('aria-current', 'page');
  await expect(tree.getByTestId('nav-volumes')).toContainText('3');
  // the config sections are tabs inside the page; the Sites tree lists the containers (like the cloud)
  await expect(page.getByTestId('tree-app-c-hello-api-2')).toBeVisible();
  await expect(page.getByTestId('breadcrumb')).toContainText('app');
  await expect(page.getByTestId('breadcrumb')).toContainText('Config');
  await expect(page.getByTestId('breadcrumb')).toContainText('Containers');
  await shot(page, 'd-nav-site', false);

  await page.locator('main').getByRole('link', { name: 'hello-api-2', exact: true }).first().click();
  await page.waitForURL(/containers\/hello-api-2$/);
  await expect(page.getByTestId('breadcrumb')).toContainText('hello-api-2');
  await page.getByTestId('breadcrumb').getByRole('link', { name: 'Containers' }).click();
  await page.waitForURL(/config\/containers$/);

  // edits survive moving between the sections of the site (shared working copy, no prompt)
  await tree.getByTestId('nav-settings').click();
  await page.getByRole('button', { name: 'Increase agent GUI alias port' }).click();
  await expect(tree.getByTestId('nav-settings').locator('.edited')).toBeVisible();
  await tree.getByTestId('nav-edge').click();
  await tree.getByTestId('nav-settings').click();
  await expect(page.locator('#sh-alias')).toHaveValue('8091');

  // per-site switch keeps the section
  await page.getByTestId('tree-toggle-db').click();
  await page.getByTestId('tree-db-settings').click();
  await page.waitForURL(/\/app\/db\/config\/settings$/);

  // old links
  await page.goto('/_opm/app/app/config?tab=edge');
  await page.getByTestId('actor-email').fill('tester@acme.example');
  await page.getByTestId('actor-continue').click();
  await page.waitForURL(/\/app\/app\/config\/edge$/);
  await page.goto('/_opm/app/db/config?tab=site');
  await page.getByTestId('actor-email').fill('tester@acme.example');
  await page.getByTestId('actor-continue').click();
  await page.waitForURL(/\/app\/db\/config\/settings$/);
  // status pages are a site scope too
  await nav(page, 'status');
  await expect(page.locator('header.top')).toHaveCount(0);
  // Change person from the sidebar footer: the popup is on top
  await page.getByTestId('change-person').click();
  await expectOnTop(page, '[data-testid="actor-popup"] .inner');
  await shot(page, 'd-nav-profile', false);
});

test('Backups: volumes per site (size, back up, used by, last snapshot, orphan, host paths), snapshot detail + rollback plan by volumeSlug', async ({ page }) => {
  await login(page, '/_opm/app/backups');
  const app = page.getByTestId('volumes-app');
  await expect(page.getByTestId('sv-app-api-env')).toContainText('hello-api-2');
  await expect(page.getByTestId('sv-app-api-env')).toContainText('on');
  await expect(page.getByTestId('sv-app-hello-api-2-data')).toContainText('off');
  await expect(page.getByTestId('sv-app-hello-api-old')).toContainText('orphan');
  await expect(page.getByTestId('sv-app-hello-api-old')).toContainText('never');
  await expect(page.getByTestId('hostpaths-app')).toContainText('/srv/hello/logs');
  await expect(page.getByTestId('hostpaths-app')).toContainText('never backed up');
  await expect(page.getByTestId('sv-db-redis-data')).toContainText('redis');
  await expect(app.locator('tr', { hasText: 'api-env' })).not.toContainText('never');
  await page.getByTestId('site-volumes').scrollIntoViewIfNeeded();
  await shot(page, 'd-backups-volumes');

  // snapshot detail: volumes by volumeSlug (incl. a pre-round-D snapshot whose volumes carry `name`)
  const rows = page.getByTestId('snapshots').locator('tbody tr');
  await rows.last().getByRole('button', { name: 'Details' }).click();
  const det = page.getByTestId('snapshot-detail');
  await expect(det.getByTestId('detail-vol-app-hello-api-data')).toBeVisible();
  await expect(det.getByTestId('detail-vol-db-redis-data')).toContainText('sites/db/volumes/redis-data.tar.zst');
  await shot(page, 'd-snapshot-detail', false);
  await det.getByRole('button', { name: 'Close' }).last().click();

  // rollback plan: restored by volumeSlug; kept = declared now but not in the snapshot (backup off)
  const row = page.getByTestId('snapshots').locator('tr', { hasText: 'Before changing the theme' });
  await row.getByRole('button', { name: 'Roll back' }).click();
  const dr = page.getByTestId('rollback-drawer');
  await expect(dr.getByTestId('restored-app-api-env')).toBeVisible();
  await expect(dr.getByTestId('restored-db-redis-data')).toBeVisible();
  await expect(dr.getByTestId('kept-app-hello-api-2-data')).toContainText('backup off');
  await shot(page, 'd-rollback-plan', false);
});

test('Backups: an unreachable site shows an error state for its volumes', async ({ page }) => {
  await knobs(page, { 'opm.mock.down': 'db' });
  await login(page, '/_opm/app/backups');
  await expect(page.getByTestId('site-volumes-db').getByTestId('error-state')).toContainText('does not answer');
  await expect(page.getByTestId('sv-app-api-env')).toBeVisible();
});
