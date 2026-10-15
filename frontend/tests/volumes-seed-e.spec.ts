import { test, expect } from '@playwright/test';
import { login, pickSource } from './helpers';

/** Round E (B): volume kind (folder / single file) + starter content, in the volume drawer. */
const SITE = '/app/templates/TplNorthwindClient04/sites/site-01';

test.describe('round E: volume kind + starter content', () => {
  test('folder seeded from files, single-file volume, archive upload, path checks, table columns', async ({ page }) => {
    await login(page, 'admin', `${SITE}/volumes`);
    await expect(page.getByTestId('volume-kind-nodered-data')).toHaveText('folder');
    // §21 #101: the mock template was migrated (migrate-h): own content instead of Files
    await expect(page.getByTestId('volume-seed-nodered-data')).toHaveText('2 files');

    // edit shows "Own content" + a link to the content page (no Files pickers any more)
    await page.getByTestId('vol-edit-nodered-data').click();
    const m = page.getByTestId('volume-modal');
    await expect(m.getByTestId('vm-seed-own')).toContainText('2 files');
    await expect(m.getByTestId('vm-seed-edit-link')).toBeVisible();
    await expect(m.getByTestId('vm-seed-mode-files')).toHaveCount(0);
    await expect(m.getByTestId('vm-seed-note')).toContainText('created or empty');
    await m.getByTestId('vm-cancel').click();

    // a single-file volume: warning about rename-saving apps; own content (edited on the content page)
    await page.getByTestId('add-volume').click();
    await m.getByTestId('vm-slug').fill('nodered-settings-file');
    await m.getByTestId('vm-kind-file').click();
    await expect(m.getByTestId('vm-file-warning')).toContainText('Node-RED');
    await m.getByTestId('vm-seed-mode-own').click();
    await expect(m.getByTestId('vm-seed-own')).toContainText('No content yet');
    await page.waitForTimeout(350);
    await page.screenshot({ path: 'screenshots/e-mock-volume-single-file.png' });
    await m.getByTestId('vm-save').click();
    await expect(m).toHaveCount(0);
    await expect(page.getByTestId('volume-kind-nodered-settings-file')).toHaveText('single file');
    await expect(page.getByTestId('volume-seed-nodered-settings-file')).toHaveText('Empty');

    // "Add & edit content" declares the volume and opens its content page
    await page.getByTestId('add-volume').click();
    await m.getByTestId('vm-slug').fill('flows');
    await m.getByTestId('vm-seed-mode-own').click();
    await m.getByTestId('vm-save-edit').click();
    await expect(page.getByTestId('volume-content-page')).toHaveAttribute('data-volume', 'flows');
    await page.locator('a.back').click();
    await expect(page.getByTestId('volume-seed-flows')).toHaveText('Empty');

    // a folder from a .tar.gz (purpose seed-archive upload)
    await page.getByTestId('add-volume').click();
    await m.getByTestId('vm-slug').fill('web-assets');
    await m.getByTestId('vm-seed-mode-archive').click();
    await m.getByTestId('vm-seed-archive').setInputFiles({ name: 'not-an-archive.zip', mimeType: 'application/zip', buffer: Buffer.from('x') });
    await expect(m.getByTestId('vm-seed-archive-error')).toContainText('.tar.gz');
    await m.getByTestId('vm-seed-archive').setInputFiles({ name: 'assets.tar.gz', mimeType: 'application/gzip', buffer: Buffer.from([0x1f, 0x8b, 8, 0, 0, 0, 0, 0, 0, 3, 1, 2, 3, 4]) });
    await expect(m.getByTestId('vm-seed-archive-name')).toContainText('assets.tar.gz');
    await m.getByTestId('vm-save').click();
    await expect(page.getByTestId('volume-seed-web-assets')).toHaveText('archive assets.tar.gz');
    await page.screenshot({ path: 'screenshots/e-mock-volumes-table.png' });

    // container page: the dropdown shows the kind; a single-file volume at a folder path → V_VOLUME_KIND
    await page.getByTestId('tree-site-01-c-nodered').click(); // in-app navigation keeps the site working copy
    await page.waitForURL('**/containers/nodered');
    await page.getByTestId('add-mount').click();
    await page.getByTestId('extra-1-source').click();
    await expect(page.getByTestId('extra-1-source-list').locator('[data-value="volume:nodered-settings-file"]')).toContainText('single file');
    await page.keyboard.press('Escape');
    await pickSource(page, 'extra-1-source', 'volume:nodered-settings-file');
    await page.getByTestId('extra-1-mount-input').fill('/data/conf/');
    await expect(page.locator('[data-testid="extra-1-issue"][data-code="V_VOLUME_KIND"]')).toBeVisible();
    await page.getByTestId('extra-1-mount-input').fill('/data/settings.js');
    await expect(page.locator('[data-testid="extra-1-issue"][data-code="V_VOLUME_KIND"]')).toHaveCount(0);
  });
});
