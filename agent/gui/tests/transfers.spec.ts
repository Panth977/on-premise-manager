/** §21 #100 Transfers: Send (left) + Receive (right) in one module; Send → Browser | Path; the server filesystem picker. */
import { test, expect } from './fixtures';
import { login, nav, open, pickOption, shot } from './helpers';
import type { Page } from '@playwright/test';

const SCR = process.env.OPM_SCREENS_DIR;
async function snap(page: Page, name: string) {
  await shot(page, name, false);
  if (SCR) await page.screenshot({ path: `${SCR}/${name}.png` });
}

test('one Transfers module in the Agent section; two columns; states; site switch keeps it', async ({ page }) => {
  await login(page, '/_opm/app/app/status');
  await nav(page, 'transfers');
  await page.waitForURL(/\/_opm\/app\/app\/transfers$/);
  await expect(page.getByTestId('side-send')).toHaveCount(0);
  await expect(page.getByTestId('side-receive')).toHaveCount(0);
  await expect(page.getByTestId('page-title')).toHaveText('Transfers');
  const send = page.getByTestId('send-col');
  const recv = page.getByTestId('receive-col');
  const a = (await send.boundingBox())!;
  const b = (await recv.boundingBox())!;
  expect(a.x).toBeLessThan(b.x); // send left, receive right
  expect(Math.abs(a.y - b.y)).toBeLessThan(2);
  // to-cloud states (store-and-forward)
  const sent = page.getByTestId('sent');
  await expect(sent.locator('[data-state="queued"]')).toContainText('support-bundle.zip');
  await expect(sent.locator('[data-state="queued"]')).toContainText('retry 3 — cloud unreachable');
  await expect(sent.locator('[data-state="uploading"]')).toContainText('pg-dump-2026-10-08.sql.gz');
  await expect(sent.locator('[data-state="uploading"] [data-testid="state"]')).toContainText('40 %');
  await expect(sent.locator('[data-state="uploading"]')).toContainText('from /srv/backups/pg-dump-2026-10-08.sql.gz');
  await expect(sent.locator('[data-state="stored"]')).toContainText('app-logs-2026-10-05.tar.gz');
  await expect(sent.locator('[data-state="downloaded"]')).toContainText('hmi-screenshot.png');
  // to-site states
  const rec = page.getByTestId('received');
  await expect(rec.locator('[data-state="pending"]')).toContainText('calibration-2026-10.csv');
  await expect(rec.locator('[data-state="downloading"]')).toContainText('60 %');
  await expect(rec.locator('[data-state="on-site"]')).toContainText('license-2027.key');
  await expect(rec.locator('[data-state="saved-on-server"]')).toContainText('/srv/drop/plant-layout.pdf');
  await expect(rec.locator('[data-state="expired"]')).toContainText('q2-report.xlsx');
  await expect(page.getByTestId('save-TrfCalib000000000006')).toBeDisabled();
  await expect(page.getByTestId('save-TrfFirmware000000005')).toBeDisabled();
  await expect(page.getByTestId('save-TrfOldReport00000008')).toBeDisabled();
  await expect(page.getByTestId('save-TrfLicense0000000002')).toBeEnabled();
  await snap(page, 'transfers-light');
  await page.emulateMedia({ colorScheme: 'dark' });
  await snap(page, 'transfers-dark');
  await page.emulateMedia({ colorScheme: 'light' });
  // another site's Transfers from the Sites tree
  await page.getByTestId('tree-toggle-db').click();
  await page.getByTestId('tree-db-transfers').click();
  await page.waitForURL(/\/_opm\/app\/db\/transfers$/);
  await expect(page.getByTestId('sent')).toContainText('redis-dump.rdb');
  await expect(page.getByTestId('receive-col')).toContainText('No files');
  await open(page, '/_opm/app/*/transfers');
  await expect(page.getByTestId('choose-site')).toContainText('Choose a site to see transfers');
});

test('Send → Browser: upload with progress, then queued → uploading → stored', async ({ page }) => {
  await login(page, '/_opm/app/app/transfers');
  await page.getByTestId('send-open').click();
  await expect(page.getByTestId('send-choice')).toContainText('Browser');
  await expect(page.getByTestId('send-choice')).toContainText('Path');
  await snap(page, 'transfers-choice-light');
  await page.getByTestId('send-browser').click();
  await page.getByTestId('send-file-input').setInputFiles({ name: 'report.csv', mimeType: 'text/csv', buffer: Buffer.from('a,b\n1,2\n') });
  await page.getByTestId('file-go').click();
  const row = page.getByTestId('sent-row').filter({ hasText: 'report.csv' });
  await expect(row).toContainText('from the browser');
  await expect(row).toHaveAttribute('data-state', /queued|uploading|stored/);
  await expect(row).toHaveAttribute('data-state', 'stored', { timeout: 15_000 });
});

test('Send → Path: fs picker (crumbs, typed path, hidden, no permission, blocked, too large, symlinks, truncated) → queued', async ({ page }) => {
  await login(page, '/_opm/app/app/transfers');
  await page.getByTestId('send-open').click();
  await page.getByTestId('send-path').click();
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
  const row = page.getByTestId('sent-row').filter({ hasText: 'pg-dump-2026-10-07.sql.gz' });
  await expect(row).toContainText('from /srv/backups/pg-dump-2026-10-07.sql.gz');
  await expect(row).toContainText('dump for support');
  await expect(row).toHaveAttribute('data-state', 'stored', { timeout: 15_000 });
});

test('old Send / Receive URLs redirect to Transfers', async ({ page }) => {
  await login(page, '/_opm/app/app/send');
  await page.waitForURL(/\/_opm\/app\/app\/transfers$/);
  await open(page, '/_opm/app/db/receive');
  await page.waitForURL(/\/_opm\/app\/db\/transfers$/);
  await open(page, '/_opm/app/sites/app/send');
  await page.waitForURL(/\/_opm\/app\/app\/transfers$/);
  await expect(page.getByTestId('send-col')).toBeVisible();
});
