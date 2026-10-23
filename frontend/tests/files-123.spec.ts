/**
 * §21 #123 Shared files: one bucket per deployment (replaces the per-site Transfers of §21 #100).
 * List + badges, publish from the browser → in the cloud → download, a site-only file → Get into the cloud → progress →
 * the download starts by itself, unpublish → tombstone (toggle), old per-site URLs redirect, navigation, permissions.
 * Optional screenshots (OPM_FILES_SHOTS=<dir>): light, dark, 390 px.
 */
import { readFile } from 'node:fs/promises';
import { test, expect, type Page } from '@playwright/test';
import { login, expectToast, SEED } from './helpers';

const D = `/app/deployments/${SEED.deployment}`;
const URL_FILES = `${D}/shared-files`;
const SHOTS = process.env.OPM_FILES_SHOTS;
const row = (page: Page, name: string) => page.locator(`[data-testid=file-row][data-file="${name}"]`);

test.describe('shared files (§21 #123)', () => {
  test('list: badges, origin, copies; tombstones only with the toggle', async ({ page }) => {
    await login(page, 'admin', URL_FILES);
    await expect(page.getByTestId('file-row')).toHaveCount(4);
    // in the cloud (+ a copy on app)
    const lic = row(page, 'license-2027.lic');
    await expect(lic).toHaveAttribute('data-badge', 'here');
    await expect(lic.getByTestId('badge')).toHaveText('in the cloud');
    await expect(lic.getByTestId('origin')).toHaveText('the cloud (browser)');
    await expect(lic.getByTestId('copies')).toHaveText('cloud, app');
    await expect(lic).toContainText('new license for every site');
    await expect(lic).toContainText('admin@example.com');
    // published from a server path of db, not in the cloud
    const dump = row(page, 'pg_dump.sql.gz');
    await expect(dump).toHaveAttribute('data-badge', 'available');
    await expect(dump.getByTestId('origin')).toHaveText('db (/var/backups/pg_dump.sql.gz)');
    await expect(dump.getByTestId('copies')).toHaveText('db');
    await expect(dump.getByTestId('get-into-cloud')).toBeVisible();
    await expect(dump.getByTestId('download')).toHaveCount(0);
    // copies on two sites
    const fw = row(page, 'firmware-2.4.1.bin');
    await expect(fw.getByTestId('copies')).toHaveText('app, db');
    await expect(fw.getByTestId('origin')).toHaveText('app (browser)');
    // nobody holds a copy: download disabled with a reason
    const gone = row(page, 'gateway-notes.txt');
    await expect(gone).toHaveAttribute('data-badge', 'unavailable');
    await expect(gone.getByTestId('copies')).toHaveText('no copy');
    await expect(gone.getByTestId('download-unavailable')).toBeDisabled();
    await expect(gone.getByTestId('download-unavailable')).toHaveAttribute('data-tip', /No site holds a copy/);
    // the tombstone shows with the toggle
    await expect(row(page, 'calibration-old.csv')).toHaveCount(0);
    await page.getByTestId('show-unpublished').click();
    const old = row(page, 'calibration-old.csv');
    await expect(old).toHaveAttribute('data-badge', 'unpublished');
    await expect(old).toContainText('by admin@example.com');
    await expect(old.getByTestId('unpublish')).toHaveCount(0);
    await expect(old.getByTestId('download')).toHaveCount(0);
    // a file in the cloud downloads
    const dl = page.waitForEvent('download');
    await lic.getByTestId('download').click();
    expect((await dl).suggestedFilename()).toBe('license-2027.lic');
  });

  test('publish from the browser → in the cloud → download', async ({ page }) => {
    await login(page, 'admin', URL_FILES);
    await expect(page.getByTestId('file-row').first()).toBeVisible();
    const content = 'hello every site\n'.repeat(200);
    await page.getByTestId('publish-file').click();
    await page.getByTestId('send-file-input').setInputFiles({ name: 'hello.txt', mimeType: 'text/plain', buffer: Buffer.from(content) });
    await page.getByTestId('send-note').fill('for everybody');
    await page.getByTestId('publish-submit').click();
    await expectToast(page, 'success', 'hello.txt published');
    const r = row(page, 'hello.txt');
    await expect(r).toHaveAttribute('data-badge', 'here');
    await expect(r).toContainText('for everybody');
    await expect(r.getByTestId('origin')).toHaveText('the cloud (browser)');
    await page.getByTestId('refresh').click();
    await expect(r.getByTestId('copies')).toHaveText('cloud');
    const dl = page.waitForEvent('download');
    await r.getByTestId('download').click();
    const d = await dl;
    expect(d.suggestedFilename()).toBe('hello.txt');
    expect(await readFile((await d.path())!, 'utf8')).toBe(content);
  });

  test('a site-only file: Get into the cloud → progress → the download starts by itself', async ({ page }) => {
    await login(page, 'admin', URL_FILES);
    const r = row(page, 'pg_dump.sql.gz');
    const dl = page.waitForEvent('download', { timeout: 30_000 });
    await r.getByTestId('get-into-cloud').click();
    await expect(r).toHaveAttribute('data-badge', 'getting');
    await expect(r.getByTestId('fetch-progress')).toBeVisible();
    await expect(r.getByTestId('download-wait')).toBeDisabled();
    await expect(r.getByTestId('fetch-progress')).toContainText(/from db · \d+ %/, { timeout: 15_000 });
    expect((await dl).suggestedFilename()).toBe('pg_dump.sql.gz');
    await expect(r).toHaveAttribute('data-badge', 'here');
    await expect(r.getByTestId('copies')).toHaveText('cloud, db');
  });

  test('unpublish → gone from the list, visible as a tombstone', async ({ page }) => {
    await login(page, 'admin', URL_FILES);
    const r = row(page, 'firmware-2.4.1.bin');
    await r.getByTestId('unpublish').click();
    await expect(page.getByTestId('confirm-dialog')).toContainText('every site');
    await page.getByTestId('confirm-ok').click();
    await expectToast(page, 'success', 'firmware-2.4.1.bin unpublished');
    await expect(r).toHaveCount(0);
    await page.getByTestId('show-unpublished').click();
    await expect(r).toHaveAttribute('data-badge', 'unpublished');
    await expect(r.getByTestId('copies')).toHaveText('—');
  });

  test('old per-site Transfers / Send / Receive URLs redirect to Shared files', async ({ page }) => {
    await login(page, 'admin', D);
    await page.goto(`${D}/sites/app/transfers?x=1`);
    await expect(page).toHaveURL(new RegExp(`${URL_FILES}\\?x=1$`));
    await expect(page.getByTestId('files-table')).toBeVisible();
    await page.goto(`${D}/sites/db/send`);
    await expect(page).toHaveURL(new RegExp(`${URL_FILES}$`));
    await page.goto(`${D}/sites/db/receive`);
    await expect(page).toHaveURL(new RegExp(`${URL_FILES}$`));
    await expect(page.getByTestId('file-row').first()).toBeVisible();
  });

  test('one deployment-level sidebar entry; no per-site Transfers links', async ({ page }) => {
    await login(page, 'admin', `${D}/sites`);
    await expect(page.getByTestId('tree-app-transfers')).toHaveCount(0);
    await expect(page.getByTestId('site-transfers')).toHaveCount(0);
    await page.getByTestId('nav-shared-files').click();
    await expect(page).toHaveURL(new RegExp(`${URL_FILES}$`));
    await expect(page.getByTestId('nav-shared-files')).toHaveClass(/on/);
  });

  test('permissions: no read → no access, no sidebar entry; read only → no publish / unpublish', async ({ page }) => {
    await login(page, 'viewer', URL_FILES);
    await expect(page.getByText('You need deployment.transfers.read')).toBeVisible();
    await expect(page.getByTestId('nav-shared-files')).toHaveCount(0);
    await expect(page.getByTestId('publish-file')).toHaveCount(0);
    // give the viewer deployment.transfers.read in the mock DB, then load the page again
    await page.evaluate((uid) => {
      const db = JSON.parse(localStorage.getItem('opm.mock.db')!);
      db.accounts[uid].access.push({ resource: 'deployment.transfers', actions: ['read'], scope: { kind: 'all' } });
      localStorage.setItem('opm.mock.db', JSON.stringify(db));
      // a pending debounced save of the page's in-memory DB must not overwrite the edit before the reload
      const set = Storage.prototype.setItem;
      Storage.prototype.setItem = function (k: string, v: string) {
        if (k !== 'opm.mock.db') set.call(this, k, v);
      };
    }, SEED.uidViewer);
    await page.goto(URL_FILES);
    await expect(page.getByTestId('file-row')).toHaveCount(4);
    await expect(page.getByTestId('nav-shared-files')).toBeVisible();
    await expect(page.getByTestId('publish-file')).toHaveCount(0);
    await expect(page.getByTestId('unpublish')).toHaveCount(0);
    await expect(row(page, 'license-2027.lic').getByTestId('download')).toBeVisible();
    await expect(row(page, 'pg_dump.sql.gz').getByTestId('get-into-cloud')).toBeVisible();
  });

  test('mobile 390 px: no page overflow', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await login(page, 'admin', URL_FILES);
    await expect(page.getByTestId('file-row').first()).toBeVisible();
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(overflow).toBeLessThanOrEqual(0);
  });

  test('screenshots (light, dark, 390 px)', async ({ page }) => {
    test.skip(!SHOTS, 'set OPM_FILES_SHOTS=<dir>');
    const shot = async (p: Page, name: string) => {
      await p.waitForTimeout(400);
      await p.screenshot({ path: `${SHOTS}/${name}.png`, fullPage: true });
    };
    await page.setViewportSize({ width: 1440, height: 900 });
    await login(page, 'admin', URL_FILES);
    await expect(page.getByTestId('file-row').first()).toBeVisible();
    await page.emulateMedia({ colorScheme: 'light' });
    await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'light'));
    await shot(page, 'cloud-files-light');
    await page.getByTestId('publish-file').click();
    await shot(page, 'cloud-files-publish-modal');
    await page.keyboard.press('Escape');
    await row(page, 'pg_dump.sql.gz').getByTestId('get-into-cloud').click();
    await expect(row(page, 'pg_dump.sql.gz').getByTestId('fetch-progress')).toBeVisible();
    await page.emulateMedia({ colorScheme: 'dark' });
    await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'dark'));
    await shot(page, 'cloud-files-dark');
    await page.setViewportSize({ width: 390, height: 844 });
    await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'light'));
    await page.emulateMedia({ colorScheme: 'light' });
    await shot(page, 'cloud-files-mobile');
  });
});
