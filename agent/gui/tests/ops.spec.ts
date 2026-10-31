import { test, expect } from './fixtures';
import { knobs, login, open, shot, nav } from './helpers';

test('sites home: tiles + mesh links', async ({ page }) => {
  await login(page);
  await expect(page.getByTestId('tile-app')).toContainText('this site');
  await expect(page.getByTestId('tile-db')).toContainText('1/1 ready');
  await expect(page.getByTestId('mesh')).toContainText('3 ms');
  await shot(page, 'sites');
});

test('backups: take, note, download, import (bad + good), roll back with the 60 s + ACCEPT flow', async ({ page }) => {
  await knobs(page, { 'opm.mock.waitSec': '2' });
  await login(page, '/_opm/app/backups');
  await expect(page.getByTestId('snapshots')).toContainText('Before changing the theme');
  await shot(page, 'backups');
  await page.getByTestId('take-snapshot').click();
  await page.getByTestId('snap-note').fill('before the night shift');
  await page.getByTestId('snap-go').click();
  await expect(page.getByText('Snapshot running')).toBeVisible();
  await expect(page.getByText('Snapshot complete')).toBeVisible({ timeout: 15_000 });
  await expect(page.getByTestId('snapshots')).toContainText('before the night shift');

  await page.getByRole('button', { name: 'Download' }).first().click();
  await expect(page.getByText('with secret values')).toBeVisible();
  await page.getByLabel(/Include images/).check();
  const dl = page.waitForEvent('download');
  await page.getByTestId('dl-go').click();
  expect((await dl).suggestedFilename()).toMatch(/\.opmsnap$/);

  await page.getByTestId('import-input').setInputFiles({ name: 'bad.opmsnap', mimeType: 'application/x-tar', buffer: Buffer.from('not a snapshot') });
  await expect(page.getByText('Import failed')).toBeVisible();
  await expect(page.getByText('manifest.json is missing')).toBeVisible();
  await shot(page, 'backups-import-error');
  await page.getByRole('button', { name: 'OK' }).click();
  await page.getByTestId('import-input').setInputFiles({ name: 'good.opmsnap', mimeType: 'application/x-tar', buffer: Buffer.from('opmsnap/1 exported elsewhere') });
  await expect(page.getByText('Snapshot imported')).toBeVisible();

  const row = page.getByTestId('snapshots').locator('tr', { hasText: 'Before changing the theme' });
  await row.getByRole('button', { name: 'Roll back' }).click();
  const drawer = page.getByTestId('rollback-drawer');
  await expect(drawer).toContainText('is lost');
  await expect(drawer.getByTestId('gate')).toBeVisible();
  await shot(page, 'rollback-plan');
  await drawer.getByTestId('rollback-go').click();
  await expect(page.getByTestId('accept-input')).toBeVisible({ timeout: 8000 });
  await page.getByTestId('accept-input').fill('ACCEPT');
  await page.getByTestId('rollback-message').fill('Roll back to before the theme change');
  await page.getByTestId('start-update').click();
  await expect(page.getByTestId('update-progress')).toBeVisible();
  await expect(page.getByTestId('update-result')).toContainText('Rollback applied', { timeout: 30_000 });
  await shot(page, 'rollback-applied');
  await expect(page.getByTestId('head-seq')).toHaveText('head seq 8', { timeout: 10_000 });
});

test('status: containers, uptime heatmaps, certificate renew, local CA, logs', async ({ page }) => {
  await login(page, '/_opm/app/*/status');
  await expect(page.getByTestId('containers-status')).toContainText('portainer');
  await expect(page.getByTestId('containers-status')).toContainText('not managed');
  await expect(page.getByTestId('uptime')).toBeVisible();
  await page.getByRole('tab', { name: '30 days · 6 h' }).click();
  await shot(page, 'status-all');
  await page.getByTestId('renew-hello.acme.internal').click();
  await page.getByTestId('confirm-ok').click();
  await expect(page.getByText('Certificate renewed for hello.acme.internal')).toBeVisible();
  const dl = page.waitForEvent('download');
  await page.getByRole('button', { name: 'Download local CA' }).click();
  expect((await dl).suggestedFilename()).toContain('local-ca.crt');
  await page.getByLabel('Search the log').fill('ERROR');
  await expect(page.getByTestId('logs')).toContainText('ERROR redis');
  await expect(page.getByTestId('logs')).not.toContainText('GET /healthz');
});

test('status of an unreachable site shows the stale banner', async ({ page }) => {
  await knobs(page, { 'opm.mock.down': 'db' });
  await login(page, '/_opm/app/db/status');
  await expect(page.getByText('does not answer — its data below is the last cached state')).toBeVisible();
  await shot(page, 'status-db-stale');
});

test('activities: filter, drawer, load more, bundle download', async ({ page }) => {
  await login(page, '/_opm/app/app/activities');
  await expect(page.getByTestId('activities')).toContainText('actor.declared');
  await page.getByTestId('load-more').click();
  await expect.poll(() => page.getByTestId('activities').locator('tbody tr').count()).toBeGreaterThan(140);
  await expect(page.getByText('end of the log')).toBeVisible();
  await page.getByTestId('filter-type').fill('update.');
  await expect(page.getByTestId('activities').locator('tbody tr').first()).toContainText('update.');
  await page.getByTestId('activities').locator('tbody tr').first().click();
  await expect(page.getByTestId('activity-drawer')).toContainText('prevHash');
  await shot(page, 'activity-drawer');
  await page.keyboard.press('Escape');
  await page.getByTestId('download-bundle').click();
  await page.waitForURL(/activities\/download/);
  await page.getByLabel(/Include debug log/).check();
  await expect(page.getByTestId('manifest-preview')).toContainText('opmlog/1');
  await shot(page, 'activities-download');
  const dl = page.waitForEvent('download');
  await page.getByTestId('download-opmlog').click();
  expect((await dl).suggestedFilename()).toMatch(/^DepHelloWorld0000001-app-1-\d+\.opmlog$/);
});

test('debug log: filters, merged *, load older', async ({ page }) => {
  await login(page, '/_opm/app/*/debug');
  await expect(page.getByTestId('debug-lines')).toBeVisible();
  await page.getByRole('radiogroup', { name: 'Minimum level' }).getByRole('radio', { name: '≥ warn' }).click();
  await expect(page.getByTestId('debug-lines')).not.toContainText(' info ');
  await page.getByRole('radiogroup', { name: 'Minimum level' }).getByRole('radio', { name: '≥ debug' }).click();
  await page.getByTestId('load-older').click();
  await shot(page, 'debug');
});

test('files (§21 #123): one Publish → browser | server path (fs picker); save on server (conflict) + download', async ({ page }) => {
  await login(page, '/_opm/app/app/transfers');
  await page.getByTestId('publish-open').click();
  await page.getByTestId('publish-path').click();
  await expect(page.getByTestId('fs-picker')).toBeVisible();
  await page.getByTestId('fs-path').fill('/var/log/app/app.log');
  await page.getByTestId('fs-go').click();
  await expect(page.getByTestId('fs-selected')).toHaveText('/var/log/app/app.log');
  await page.getByTestId('fs-pick').click();
  await expect(page.getByTestId('path-picked')).toHaveText('/var/log/app/app.log');
  await page.getByTestId('path-go').click();
  await expect(page.getByTestId('files')).toContainText('app.log');
  await page.getByTestId('publish-open').click();
  await page.getByTestId('publish-browser').click();
  await page.getByTestId('send-file-input').setInputFiles({ name: 'report.csv', mimeType: 'text/csv', buffer: Buffer.from('a,b\n1,2\n') });
  await page.getByTestId('file-go').click();
  await expect(page.getByTestId('files')).toContainText('report.csv');
  await shot(page, 'send');

  await expect(page.getByTestId('files')).toContainText('plant-layout.pdf');
  await page.getByTestId('save-FilPlantLayout0000000001').click();
  await page.getByTestId('save-path').fill('/srv/drop/exists');
  await page.getByTestId('save-go').click();
  await expect(page.getByText('already exists on app')).toBeVisible();
  await page.getByTestId('save-path').fill('/srv/drop/plant-layout.pdf');
  await page.getByTestId('save-go').click();
  await expect(page.getByTestId('save-modal')).toBeHidden();
  const dl = page.waitForEvent('download');
  await page.getByTestId('dl-FilManual000000000000008').click();
  await dl;
  await shot(page, 'receive');
});

test('internet tunnel: start in this tab → real worker carries requests (inline + chunked) → stop', async ({ page }) => {
  await login(page, '/_opm/app/internet');
  await expect(page.getByTestId('tunnel-free')).toBeVisible();
  await shot(page, 'internet-free');
  await page.getByTestId('tunnel-start').click();
  await expect(page.getByTestId('tunnel-active')).toBeVisible();
  await expect(page.getByTestId('tunnel-security')).toContainText('end-to-end encrypted');
  await expect(page.getByTestId('tunnel-encrypted')).toHaveText('End to end');
  // the tab knows one route: what is inside stays between the agent and the cloud
  await expect(page.getByTestId('tunnel-log')).toContainText('/v1/agent/sealed?mock=big', { timeout: 20_000 });
  await expect(page.getByTestId('tunnel-log')).not.toContainText('/v1/agent/poll');
  await expect.poll(async () => Number(await page.getByTestId('tunnel-requests').innerText()), { timeout: 20_000 }).toBeGreaterThanOrEqual(4);
  await expect(page.getByTestId('tunnel-log')).toContainText('done');
  await shot(page, 'internet-active');
  // the worker keeps running on other pages (banner)
  await nav(page, 'sites');
  await expect(page.getByText('This tab gives internet to the deployment')).toBeVisible();
  await nav(page, 'internet');
  await page.getByTestId('tunnel-stop').click();
  await expect(page.getByTestId('tunnel-free')).toBeVisible();
});

test('internet tunnel held by another tab → revoke → start', async ({ page }) => {
  await knobs(page, { 'opm.mock.otherTab': '1' });
  await login(page, '/_opm/app/internet');
  await expect(page.getByTestId('tunnel-other')).toContainText('mira@acme.example');
  await shot(page, 'internet-other-tab');
  await page.getByTestId('tunnel-revoke').click();
  await page.getByTestId('confirm-ok').click();
  await expect(page.getByTestId('tunnel-free')).toBeVisible();
  await page.getByTestId('tunnel-start').click();
  await expect(page.getByTestId('tunnel-active')).toBeVisible();
});

test('fresh install (no head): empty states everywhere', async ({ page }) => {
  await knobs(page, { 'opm.mock.noHead': '1' });
  await login(page);
  await expect(page.getByText('This site has no configuration yet')).toBeVisible();
  await expect(page.getByTestId('head-seq')).toHaveText('no head yet');
  await open(page, '/_opm/app/variables');
  await expect(page.getByText('No configuration yet')).toBeVisible();
  await shot(page, 'no-head-variables');
  await open(page, '/_opm/app/updates');
  await expect(page.getByTestId('empty-state')).toContainText('No cloud commit yet');
  // the empty state explains the next step: cloud console → Commit… (§21 #104), with the console URL + head state
  await expect(page.getByTestId('next-steps')).toContainText('Commit…');
  await expect(page.getByTestId('console-url')).toHaveText('https://opm.example.com');
  await expect(page.getByTestId('site-head-state')).toContainText('no head yet');
  await shot(page, 'redesign-updates-empty');
});

test('dark mode', async ({ page }) => {
  await page.emulateMedia({ colorScheme: 'dark' });
  await login(page);
  await shot(page, 'dark-sites');
  await open(page, '/_opm/app/app/branch');
  await expect(page.getByTestId('cloud-card')).toBeVisible();
  await shot(page, 'dark-proposal');
  await open(page, '/_opm/app/app/status');
  await shot(page, 'dark-status');
});
