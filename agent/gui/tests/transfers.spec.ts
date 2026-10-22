/** §21 #123 Files: one shared bucket per deployment, as seen by a site (URL /transfers); publish (browser | server path),
 * download (a copy first when it is not here), save on server, get a copy here, drop this site's copy, unpublish. */
import { test, expect } from './fixtures';
import { login, nav, open, shot } from './helpers';
import type { Page } from '@playwright/test';

const SCR = process.env.OPM_SCREENS_DIR;
async function snap(page: Page, name: string) {
  await shot(page, name, false);
  if (SCR) await page.screenshot({ path: `${SCR}/${name}.png` });
}
const row = (page: Page, name: string) => page.locator(`[data-testid="file-row"][data-file="${name}"]`);
const toast = (page: Page) => page.getByTestId('toast');

test('one Files module (URL /transfers): the shared list with every badge, as seen by app and by db; "*" asks for a site', async ({ page }) => {
  await login(page, '/_opm/app/app/status');
  await nav(page, 'transfers');
  await page.waitForURL(/\/_opm\/app\/app\/transfers$/);
  await expect(page.getByTestId('page-title')).toHaveText('Shared files');
  await expect(page.getByTestId('tree-app-transfers')).toHaveText('Shared files');
  // here (cloud + app)
  await expect(row(page, 'plant-layout.pdf')).toHaveAttribute('data-badge', 'here');
  await expect(row(page, 'plant-layout.pdf').getByTestId('copies')).toHaveText(/copies: cloud, app\s*· here/);
  await expect(row(page, 'plant-layout.pdf').getByTestId('origin')).toHaveText('from the cloud (browser)');
  await expect(row(page, 'plant-layout.pdf')).toContainText('Updated floor plan');
  // on a peer only
  await expect(row(page, 'redis-dump.rdb')).toHaveAttribute('data-badge', 'available');
  await expect(row(page, 'redis-dump.rdb').getByTestId('hint')).toContainText('Can be copied from db over the mesh');
  await expect(row(page, 'redis-dump.rdb').getByTestId('origin')).toHaveText('from db (/var/lib/redis/dump.rdb)');
  // the cloud only
  await expect(row(page, 'license-2027.key')).toHaveAttribute('data-badge', 'available');
  await expect(row(page, 'license-2027.key').getByTestId('hint')).toContainText('Can be copied from the cloud');
  await expect(row(page, 'license-2027.key').getByTestId('copies')).toHaveText('copies: cloud');
  // still uploading into the cloud: nothing to copy yet
  await expect(row(page, 'sensor-firmware-v3.2.bin')).toHaveAttribute('data-badge', 'uploading');
  await expect(page.getByTestId('dl-FilFirmware0000000000005')).toBeDisabled();
  // a failed copy
  await expect(row(page, 'calibration-2026-10.csv')).toHaveAttribute('data-badge', 'failed');
  await expect(row(page, 'calibration-2026-10.csv').getByTestId('hint')).toContainText('connection reset by peer');
  // no copy anywhere
  await expect(row(page, 'old-gateway-config.tar')).toHaveAttribute('data-badge', 'unavailable');
  await expect(page.getByTestId('dl-FilGatewayCfg00000000007')).toBeDisabled();
  await expect(page.getByTestId('dl-FilGatewayCfg00000000007')).toHaveAttribute('title', /cannot be downloaded/);
  await expect(page.getByTestId('save-FilGatewayCfg00000000007')).toBeDisabled();
  // tombstones are hidden by default
  await expect(row(page, 'q2-report.xlsx')).toHaveCount(0);
  // nothing downloads by itself: here = Drop, not here = Get a copy here
  await expect(page.getByTestId('drop-FilPlantLayout0000000001')).toBeVisible();
  await expect(page.getByTestId('get-FilPlantLayout0000000001')).toHaveCount(0);
  await expect(page.getByTestId('get-FilRedisDump000000000003')).toBeVisible();
  await snap(page, 'files-light');
  await page.emulateMedia({ colorScheme: 'dark' });
  await snap(page, 'files-dark');
  await page.emulateMedia({ colorScheme: 'light' });
  // the same list on db, with db's copies
  await page.getByTestId('tree-toggle-db').click();
  await page.getByTestId('tree-db-transfers').click();
  await page.waitForURL(/\/_opm\/app\/db\/transfers$/);
  await expect(row(page, 'redis-dump.rdb')).toHaveAttribute('data-badge', 'here');
  await expect(row(page, 'plant-layout.pdf')).toHaveAttribute('data-badge', 'available');
  await expect(row(page, 'plant-layout.pdf').getByTestId('hint')).toContainText('from app over the mesh');
  await expect(row(page, 'calibration-2026-10.csv')).toHaveAttribute('data-badge', 'available'); // the failed job was app's
  await open(page, '/_opm/app/*/transfers');
  await expect(page.getByTestId('choose-site')).toContainText('Choose a site to see shared files');
  await page.getByTestId('choose-this-site').click();
  await page.waitForURL(/\/_opm\/app\/app\/transfers$/);
  await expect(page.getByTestId('files')).toBeVisible();
});

test('Publish → From this browser: upload with progress, then listed here', async ({ page }) => {
  await login(page, '/_opm/app/app/transfers');
  await page.getByTestId('publish-open').click();
  await expect(page.getByTestId('publish-choice')).toContainText('From this browser');
  await expect(page.getByTestId('publish-choice')).toContainText('From the server');
  await snap(page, 'files-choice-light');
  await page.getByTestId('publish-browser').click();
  await page.getByTestId('send-file-input').setInputFiles({ name: 'report.csv', mimeType: 'text/csv', buffer: Buffer.from('a,b\n1,2\n') });
  await page.getByTestId('send-note').fill('numbers for review');
  await page.getByTestId('file-go').click();
  await expect(page.getByTestId('browser-upload')).toContainText('report.csv');
  const r = row(page, 'report.csv');
  await expect(r).toHaveAttribute('data-badge', 'here');
  await expect(r.getByTestId('origin')).toHaveText('from app (browser)');
  await expect(r).toContainText('numbers for review');
  await expect(r).toContainText(/by tester@acme\.example/);
  await expect(page.getByTestId('browser-upload')).toHaveCount(0);
  // every site sees it (db: on a peer)
  await page.getByTestId('tree-toggle-db').click();
  await page.getByTestId('tree-db-transfers').click();
  await page.waitForURL(/\/_opm\/app\/db\/transfers$/);
  await expect(row(page, 'report.csv')).toHaveAttribute('data-badge', 'available');
  await expect(row(page, 'report.csv').getByTestId('hint')).toContainText('from app over the mesh');
});

test('Download of a file only a peer holds: copy with progress, then the browser download starts by itself', async ({ page }) => {
  await login(page, '/_opm/app/app/transfers');
  const r = row(page, 'redis-dump.rdb');
  await expect(r).toHaveAttribute('data-badge', 'available');
  const dl = page.waitForEvent('download', { timeout: 20_000 });
  await page.getByTestId('dl-FilRedisDump000000000003').click();
  await expect(toast(page).filter({ hasText: 'Getting redis-dump.rdb onto app' })).toBeVisible();
  await expect(r).toHaveAttribute('data-badge', 'getting');
  await expect(r.getByTestId('hint')).toContainText('Copying from db onto app');
  await expect(r.getByTestId('badge')).toContainText(/getting a copy\s+\d+ %/);
  await snap(page, 'files-getting');
  expect((await dl).suggestedFilename()).toBe('redis-dump.rdb');
  await expect(r).toHaveAttribute('data-badge', 'here');
  await expect(r.getByTestId('copies')).toContainText('copies: db, app');
});

test('Get a copy here (no browser download) and Try again after a failed copy', async ({ page }) => {
  await login(page, '/_opm/app/app/transfers');
  let downloads = 0;
  page.on('download', () => downloads++);
  const r = row(page, 'calibration-2026-10.csv');
  await expect(page.getByTestId('get-FilCalib00000000000000006')).toHaveText('Try again');
  await page.getByTestId('get-FilCalib00000000000000006').click();
  await expect(r).toHaveAttribute('data-badge', 'getting');
  await expect(r.getByTestId('hint')).toContainText('Copying from the cloud onto app');
  await expect(r).toHaveAttribute('data-badge', 'here', { timeout: 15_000 });
  expect(downloads).toBe(0);
});

test('Save on server: a file that is not here is copied first; an existing path is refused (409)', async ({ page }) => {
  await login(page, '/_opm/app/app/transfers');
  // here: the path exists → the error, then another path
  await page.getByTestId('save-FilPlantLayout0000000001').click();
  await expect(page.getByTestId('save-path')).toHaveValue('/srv/opm-drop/plant-layout.pdf');
  await expect(page.getByTestId('save-go')).toHaveText('Save');
  await page.getByTestId('save-path').fill('/srv/drop/exists');
  await page.getByTestId('save-go').click();
  await expect(page.getByTestId('save-err')).toHaveText('/srv/drop/exists already exists on app. Choose another path.');
  await page.getByTestId('save-path').fill('/srv/drop/plant-layout.pdf');
  await page.getByTestId('save-go').click();
  await expect(page.getByTestId('save-modal')).toBeHidden();
  await expect(toast(page).filter({ hasText: 'Saved plant-layout.pdf' })).toBeVisible();
  // the cloud only: copy first (progress in the modal), then save
  const r = row(page, 'license-2027.key');
  await page.getByTestId('save-FilLicense00000000000004').click();
  await expect(page.getByTestId('save-modal')).toContainText('is not on app yet');
  await expect(page.getByTestId('save-go')).toHaveText('Get a copy and save');
  await page.getByTestId('save-path').fill('/srv/opm-drop/exists');
  await page.getByTestId('save-go').click();
  await expect(page.getByTestId('save-wait')).toContainText('Getting a copy onto app');
  await snap(page, 'files-save-wait');
  await expect(page.getByTestId('save-err')).toHaveText('/srv/opm-drop/exists already exists on app. Choose another path.', { timeout: 15_000 });
  await expect(r).toHaveAttribute('data-badge', 'here');
  await expect(page.getByTestId('save-go')).toHaveText('Save');
  await page.getByTestId('save-path').fill('/srv/opm-drop/license-2027.key');
  await page.getByTestId('save-go').click();
  await expect(page.getByTestId('save-modal')).toBeHidden();
  await expect(toast(page).filter({ hasText: 'Saved license-2027.key' })).toBeVisible();
});

test('Drop this site\'s copy; the only copy cannot be dropped (409)', async ({ page }) => {
  await login(page, '/_opm/app/app/transfers');
  const r = row(page, 'operator-manual.pdf');
  await expect(r).toHaveAttribute('data-badge', 'here');
  await page.getByTestId('drop-FilManual000000000000008').click();
  await page.getByTestId('drop-confirm').getByTestId('confirm-ok').click();
  await expect(r).toHaveAttribute('data-badge', 'available');
  await expect(r.getByTestId('copies')).toHaveText('copies: cloud, db');
  // app holds the only copy of its own logs
  await page.getByTestId('drop-FilAppLogs00000000000002').click();
  await page.getByTestId('drop-confirm').getByTestId('confirm-ok').click();
  await expect(toast(page).filter({ hasText: 'Could not drop the copy' })).toContainText('This is the only copy of app-logs-2026-10-05.tar.gz');
  await expect(row(page, 'app-logs-2026-10-05.tar.gz')).toHaveAttribute('data-badge', 'here');
});

test('Unpublish (confirm) → gone from the list; "Show unpublished" lists the tombstones', async ({ page }) => {
  await login(page, '/_opm/app/app/transfers');
  await expect(row(page, 'hmi-screenshot.png')).toBeVisible();
  await page.getByTestId('unpub-FilScreenshot00000000009').click();
  await expect(page.getByTestId('unpub-confirm')).toContainText('every copy is deleted');
  await page.getByTestId('unpub-confirm').getByTestId('confirm-ok').click();
  await expect(row(page, 'hmi-screenshot.png')).toHaveCount(0);
  await page.getByTestId('show-unpublished').check();
  await expect(row(page, 'hmi-screenshot.png')).toHaveAttribute('data-badge', 'unpublished');
  await expect(row(page, 'hmi-screenshot.png').getByTestId('unpublished')).toContainText('by tester@acme.example');
  await expect(row(page, 'q2-report.xlsx')).toHaveAttribute('data-badge', 'unpublished');
  await expect(row(page, 'q2-report.xlsx').getByRole('button')).toHaveCount(0);
  await snap(page, 'files-unpublished');
  await page.getByTestId('show-unpublished').uncheck();
  await expect(row(page, 'q2-report.xlsx')).toHaveCount(0);
});

test('Publish → From the server: fs picker (crumbs, typed path, hidden, no permission, blocked, too large, symlinks, truncated) → published here', async ({ page }) => {
  await login(page, '/_opm/app/app/transfers');
  await page.getByTestId('publish-open').click();
  await page.getByTestId('publish-path').click();
  const pk = page.getByTestId('fs-picker');
  await expect(pk).toBeVisible();
  const ent = (p: string) => pk.locator(`[data-testid="fs-entry"][data-path="${p}"]`);
  // "/" — folders first; /proc is blocked; /root is not readable
  await expect(ent('/etc')).toBeVisible();
  await expect(ent('/proc').getByTestId('fs-blocked')).toContainText('kernel');
  await expect(ent('/root').getByTestId('fs-denied')).toHaveText('no permission');
  await expect(pk.getByTestId('fs-user')).toContainText('opm');
  // lazy expand inline (tree) — /etc children: shadow is blocked, passwd is selectable
  await ent('/etc').getByTestId('fs-expand').click();
  await expect(ent('/etc/shadow').getByTestId('fs-blocked')).toContainText('never sent');
  await ent('/etc/shadow').locator('.name').click();
  await expect(pk.getByTestId('fs-pick')).toBeDisabled();
  await ent('/etc/passwd').locator('.name').click();
  await expect(pk.getByTestId('fs-selected')).toHaveText('/etc/passwd');
  // navigate into a folder → breadcrumbs
  await ent('/var').locator('.name').click();
  await ent('/var/log').locator('.name').click();
  await expect(pk.getByTestId('fs-crumb')).toHaveText(['/', 'var', 'log']);
  await expect(ent('/var/log/huge-archive.log')).toContainText('larger than 2 GiB');
  await expect(ent('/var/log/locked.log').getByTestId('fs-denied')).toBeVisible();
  // symlinks: to a file (selectable), dangling (not)
  await expect(ent('/var/log/current.log')).toContainText('→ /var/log/app/app.log');
  await expect(ent('/var/log/gone.log')).toContainText('broken link');
  await ent('/var/log/current.log').locator('.name').click();
  await expect(pk.getByTestId('fs-selected')).toHaveText('/var/log/current.log');
  await snap(page, 'fs-picker-light');
  await page.emulateMedia({ colorScheme: 'dark' });
  await snap(page, 'fs-picker-dark');
  await page.emulateMedia({ colorScheme: 'light' });
  // crumb back to "/"; symlink to a folder navigates to its target
  await pk.getByTestId('fs-crumb').first().click();
  await ent('/bin').locator('.name').click();
  await expect(pk.getByTestId('fs-crumb')).toHaveText(['/', 'usr', 'bin']);
  await pk.getByTestId('fs-up').click();
  await expect(pk.getByTestId('fs-crumb')).toHaveText(['/', 'usr']);
  // unreadable folder typed → inline error, navigation still works
  await pk.getByTestId('fs-path').fill('/root');
  await pk.getByTestId('fs-go').click();
  await expect(pk.getByTestId('fs-error')).toContainText('cannot list this folder');
  await pk.getByTestId('fs-error').getByRole('button').click();
  await expect(pk.getByTestId('fs-crumb')).toHaveText(['/']);
  // missing path
  await pk.getByTestId('fs-path').fill('/nope/nothing');
  await pk.getByTestId('fs-go').click();
  await expect(pk.getByTestId('fs-path-err')).toContainText('no such folder');
  // hidden files toggle
  await pk.getByTestId('fs-path').fill('/home/op');
  await pk.getByTestId('fs-go').click();
  await expect(ent('/home/op/.bashrc')).toHaveCount(0);
  await expect(pk.getByText('Hidden (2)')).toBeVisible();
  await pk.getByTestId('fs-hidden').check();
  await expect(ent('/home/op/.bashrc')).toBeVisible();
  // truncated folder
  await pk.getByTestId('fs-path').fill('/srv/many');
  await pk.getByTestId('fs-go').click();
  await expect(pk.getByTestId('fs-truncated')).toContainText('5,000');
  // a blocked directory shows its banner
  await pk.getByTestId('fs-path').fill('/var/lib/opm');
  await pk.getByTestId('fs-go').click();
  await expect(pk.getByTestId('fs-dir-blocked')).toContainText('OPM agent data');
  // typed FILE path → its folder, file selected
  await pk.getByTestId('fs-path').fill('/srv/backups/pg-dump-2026-10-07.sql.gz');
  await pk.getByTestId('fs-go').click();
  await expect(pk.getByTestId('fs-crumb')).toHaveText(['/', 'srv', 'backups']);
  await expect(pk.getByTestId('fs-selected')).toHaveText('/srv/backups/pg-dump-2026-10-07.sql.gz');
  await pk.getByTestId('fs-pick').click();
  await expect(page.getByTestId('path-picked')).toHaveText('/srv/backups/pg-dump-2026-10-07.sql.gz');
  await page.getByTestId('send-note').fill('dump for support');
  await page.getByTestId('path-go').click();
  const r = row(page, 'pg-dump-2026-10-07.sql.gz');
  await expect(r).toHaveAttribute('data-badge', 'here');
  await expect(r.getByTestId('origin')).toHaveText('from app (/srv/backups/pg-dump-2026-10-07.sql.gz)');
  await expect(r).toContainText('dump for support');
  await expect(r.getByTestId('copies')).toContainText('copies: app');
});

test('old Send / Receive URLs redirect to Files', async ({ page }) => {
  await login(page, '/_opm/app/app/send');
  await page.waitForURL(/\/_opm\/app\/app\/transfers$/);
  await open(page, '/_opm/app/db/receive');
  await page.waitForURL(/\/_opm\/app\/db\/transfers$/);
  await open(page, '/_opm/app/sites/app/send');
  await page.waitForURL(/\/_opm\/app\/app\/transfers$/);
  await expect(page.getByTestId('files-card')).toBeVisible();
});
