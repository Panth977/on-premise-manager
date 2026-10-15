import { test, expect } from '@playwright/test';
import { login, expectToast, SEED, pickSource } from './helpers';

/**
 * Round D part 2: one copy per site. Files and site volumes have a "Location on the server" (Auto = <agentHome>/files|volumes/<slug>,
 * or a custom "./" / absolute path); the container page shows file / secret mounts as text ("mounted read-only at …",
 * Edit path), "shared copy · also used by" and the server location in the summary.
 */
const TPL = `/app/templates/${SEED.template}`;
const CORE = '/app/templates/TplNorthwindCore0002';
const SHOTS = 'screenshots';

test.describe('round D part 2: storage locations, one copy per site', () => {
  test('Files page "On the server" + drawer location (Auto / Custom path, V_LOCATION live)', async ({ page }) => {
    await login(page, 'admin', `${TPL}/files`);
    await expect(page.getByTestId('files-table').locator('th', { hasText: 'On the server' })).toBeVisible();
    await expect(page.getByTestId('file-server-banner')).toHaveText('auto <agentHome>/files/banner');
    await page.getByTestId('menu-banner').click();
    await page.getByTestId('m-edit-def').click();
    const d = page.getByTestId('file-drawer');
    await expect(d.getByTestId('file-location-mode-auto')).toHaveAttribute('aria-checked', 'true');
    await expect(d.getByTestId('file-location')).toContainText('auto · <agentHome>/files/banner');
    await d.getByTestId('file-location-mode-path').click();
    await expect(d.getByTestId('file-location-path')).toHaveValue('./files/banner');
    await expect(d.getByTestId('file-location')).toContainText('"./" = the agent home');
    await d.getByTestId('file-location-path').fill('./blobs/banner');
    await expect(d.getByTestId('file-location')).toContainText('./blobs belongs to the agent');
    await d.getByTestId('file-location-path').fill('etc/banner');
    await expect(d.getByTestId('file-location')).toContainText('must start with "./"');
    await d.getByTestId('file-location-path').fill('./files/hello/banner.txt');
    await page.waitForTimeout(250);
    await page.screenshot({ path: `${SHOTS}/d-mock-files-location.png` });
    await d.getByTestId('file-save').click();
    await expectToast(page, 'success', 'banner applied');
    await expect(page.getByTestId('file-server-banner')).toHaveText('./files/hello/banner.txt');
    await page.screenshot({ path: `${SHOTS}/d-mock-files-page.png` });
    // back to auto
    await page.getByTestId('menu-banner').click();
    await page.getByTestId('m-edit-def').click();
    await d.getByTestId('file-location-mode-auto').click();
    await d.getByTestId('file-save').click();
    await expectToast(page, 'success', 'banner applied');
    await expect(page.getByTestId('file-server-banner')).toHaveText('auto <agentHome>/files/banner');
  });

  test('container page: file rows as text + Edit path, "shared copy · also used by", server location in the summary', async ({ page }) => {
    await login(page, 'admin', `${TPL}/sites/app/containers/hello-api`);
    await expect(page.getByTestId('volumes-table')).toContainText('Inside the container');
    // the image volume "data" mounts the shared banner file instead of its volume
    await pickSource(page, 'vol-data-source', 'file:banner');
    await expect(page.getByTestId('vol-row-data')).toContainText('mounted read-only at /data');
    await expect(page.getByTestId('vol-data-ro')).toHaveCount(0);
    await expect(page.getByTestId('vol-data-mount')).toHaveCount(0); // no required-looking field
    await expect(page.getByTestId('vol-data-shared-copy')).toHaveText('shared · 2 containers'); // shared form: badge + list (§21 #102)
    await expect(page.getByTestId('vol-data-shared-copy-list')).toContainText('hello-web');
    await expect(page.getByTestId('sum-mounts')).toContainText('/data ← file banner · ro (server: auto · <agentHome>/files/banner)');
    await page.getByTestId('vol-data-edit-path').click();
    await page.getByTestId('vol-data-mount-input').fill('/etc/hello/banner.txt');
    await expect(page.getByTestId('sum-mounts')).toContainText('/etc/hello/banner.txt ← file banner');
    await page.screenshot({ path: `${SHOTS}/d-mock-files-container.png` });
    // a secret: one copy per site at <agentHome>/secrets/<slug>
    await pickSource(page, 'vol-data-source', 'secret:api-token');
    await expect(page.getByTestId('sum-mounts')).toContainText('(server: <agentHome>/secrets/api-token (0400))');
    // a volume: its location
    await pickSource(page, 'vol-data-source', 'volume:hello-api-data');
    await expect(page.getByTestId('sum-mounts')).toContainText('volume hello-api-data (server: auto · <agentHome>/volumes/hello-api-data)');
    await page.getByTestId('discard-container').click();
  });

  test('site volumes: "On the server" column + location in the modal', async ({ page }) => {
    await login(page, 'admin', `${CORE}/sites/core/volumes`);
    await expect(page.getByTestId('volume-location-cache-data')).toHaveText('./volumes/cache/redis');
    await expect(page.getByTestId('volume-location-core-api-data')).toHaveText('auto <agentHome>/volumes/core-api-data');
    await page.getByTestId('add-volume').click();
    const m = page.getByTestId('volume-modal');
    await m.getByTestId('vm-slug').fill('uploads');
    await m.getByTestId('vm-location-mode-path').click();
    await m.getByTestId('vm-location-path').fill('./files/x');
    await expect(m).toContainText('./files belongs to the agent');
    await expect(m.getByTestId('vm-save')).toBeDisabled();
    await m.getByTestId('vm-location-path').fill('/srv/uploads');
    await m.getByTestId('vm-save').click();
    await expect(m).toHaveCount(0);
    await expect(page.getByTestId('volume-location-uploads')).toHaveText('/srv/uploads');
    // edit keeps the location
    await page.getByTestId('vol-edit-uploads').click();
    await expect(m.getByTestId('vm-location-path')).toHaveValue('/srv/uploads');
    await page.waitForTimeout(250);
    await page.screenshot({ path: `${SHOTS}/d-mock-volume-location.png` });
    await m.getByTestId('vm-cancel').click();
  });
});
