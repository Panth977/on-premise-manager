/** Image volume kind (SPEC §21 #83): Folder / File control on the image editor, the container kind matrix with fix links. */
import { test, expect, type Page } from '@playwright/test';
import { login, pickSource, expectToast } from './helpers';

const SHOTS = 'screenshots';
const IMG = '/app/services/node-red/image/v1';
const C = '/app/templates/TplVolumeKinds000006/sites/site-01/containers/node-red';
const C2 = '/app/templates/TplVolumeKinds000006/sites/site-01/containers/node-red-2';
const volRow = (page: Page, name: string) => page.locator(`[data-vol-row="${name}"]`);
const issue = (page: Page, row: string, code: string) => page.locator(`[data-testid="vol-${row}-issue"][data-code="${code}"]`);
type MockImg = { volumes: { name: string; kind: string; mountPath: string }[] };
async function storedVolumes(page: Page): Promise<MockImg['volumes']> {
  const raw = (await page.evaluate(() => JSON.parse(localStorage.getItem('opm.mock.db') ?? '{}'))) as { db?: { images: Record<string, Record<string, MockImg>> }; images?: Record<string, Record<string, MockImg>> };
  return (raw.db ?? raw).images!['node-red']!['v1']!.volumes;
}

test.describe('image volume kind', () => {
  test('image editor: Folder / File per row, Auto follows the path for new rows, stored explicitly', async ({ page }) => {
    await login(page, 'admin', IMG);
    await expect(volRow(page, 'flow').getByTestId('vol-kind-file')).toHaveAttribute('aria-checked', 'true');
    await expect(volRow(page, 'settings').getByTestId('vol-kind-file')).toHaveAttribute('aria-checked', 'true');
    // a new row starts on Auto and follows the mount path
    await page.getByTestId('ri-add-vol').click();
    const row = page.getByTestId('ri-vol-row').nth(2);
    await row.getByTestId('vol-name').fill('lib');
    await row.getByTestId('vol-mount-input').fill('/data/lib');
    await expect(row.getByTestId('vol-kind-auto')).toHaveAttribute('aria-checked', 'true');
    await expect(row.getByTestId('vol-kind-auto')).toHaveText('Auto · Folder');
    await row.getByTestId('vol-mount-input').fill('/data/lib.json');
    await expect(row.getByTestId('vol-kind-auto')).toHaveText('Auto · File');
    await row.getByTestId('vol-mount-input').fill('/data/lib');
    // an explicit Folder on a file-looking path is kept
    await volRow(page, 'settings').getByTestId('vol-kind-dir').click();
    await page.waitForTimeout(300);
    await page.screenshot({ path: `${SHOTS}/volkind-image-editor.png`, fullPage: true });
    await page.getByTestId('im-save').click();
    await expectToast(page, 'success', 'Image saved');
    expect((await storedVolumes(page)).map((v) => [v.name, v.kind])).toEqual([
      ['flow', 'file'],
      ['settings', 'dir'],
      ['lib', 'dir'],
    ]);
  });

  test('register image: Auto shows the inferred kind', async ({ page }) => {
    await login(page, 'admin', '/app/services/node-red/register-image');
    await page.getByTestId('ri-add-vol').click();
    const row = page.getByTestId('ri-vol-row').first();
    await row.getByTestId('vol-name').fill('flow');
    await row.getByTestId('vol-mount-input').fill('/data/flows.json');
    await expect(row.getByTestId('vol-kind-auto')).toHaveText('Auto · File');
    await row.getByTestId('vol-kind-dir').click();
    await expect(row.getByTestId('vol-kind-dir')).toHaveAttribute('aria-checked', 'true');
  });

  test("container page: the user's case (single-file volume on a File image volume) = no warning, only the rename info", async ({ page }) => {
    await login(page, 'admin', C);
    await expect(page.getByTestId('vol-flow-imgkind')).toHaveText('File');
    await expect(page.locator('[data-code="V_VOLUME_KIND_IMAGE"]')).toHaveCount(0);
    await expect(issue(page, 'flow', 'V_FILE_VOLUME_RENAME')).toHaveCount(1);
    await expect(issue(page, 'flow', 'V_FILE_VOLUME_RENAME')).toContainText('apps that save by rename (e.g. Node-RED flows.json) fail on single-file mounts (EBUSY)');
    await expect(page.getByTestId('vol-flow-rename-tip-text')).toHaveText(/Recommended for Node-RED: a FOLDER data volume mounted at \/data/);
    await expect(issue(page, 'settings', 'V_FILE_VOLUME_RENAME')).toHaveCount(0); // read-only mount
    await page.locator('#sec-volumes').scrollIntoViewIfNeeded();
    await page.waitForTimeout(300);
    await page.locator('#sec-volumes').screenshot({ path: `${SHOTS}/volkind-container-ok.png` });
  });

  test('container page: folder volume on a File image volume = error with a one-click new single-file volume', async ({ page }) => {
    await login(page, 'admin', C);
    await pickSource(page, 'vol-flow-source', 'volume:nodered-data');
    const e = issue(page, 'flow', 'V_VOLUME_KIND_MISMATCH');
    await expect(e).toContainText('folder volume nodered-data cannot be mounted on file path /data/flows.json');
    await expect(e).toContainText('use a single-file volume, or change the image volume to Folder');
    await expect(page.getByTestId('vol-flow-fix-image')).toContainText('Set image volume flow to Folder on the image page');
    await expect(page.getByTestId('vol-flow-fix-image')).toHaveAttribute('href', /\/app\/services\/node-red\/image\/v1\?volume=flow&from=/);
    await page.locator('#sec-volumes').scrollIntoViewIfNeeded();
    await page.waitForTimeout(300);
    await page.locator('#sec-volumes').screenshot({ path: `${SHOTS}/volkind-mismatch.png` });
    await page.getByTestId('vol-flow-fix-volume').click();
    const m = page.getByTestId('volume-modal');
    await expect(m.getByTestId('vm-kind-file')).toHaveAttribute('aria-checked', 'true');
    await m.getByTestId('vm-slug').fill('nodered-flow-2');
    await m.getByTestId('vm-save').click();
    await expect(m).toHaveCount(0);
    await expect(page.getByTestId('vol-flow-source')).toContainText('nodered-flow-2');
    await expect(issue(page, 'flow', 'V_VOLUME_KIND_MISMATCH')).toHaveCount(0);
    await expect(issue(page, 'flow', 'V_FILE_VOLUME_RENAME')).toHaveCount(1);
  });

  test('container page: single-file volume on a Folder image volume = warning → image page link → set File → warning gone', async ({ page }) => {
    await login(page, 'admin', IMG);
    await volRow(page, 'flow').getByTestId('vol-kind-dir').click();
    await page.getByTestId('im-save').click();
    await expectToast(page, 'success', 'Image saved');
    await page.goto(C);
    const w = issue(page, 'flow', 'V_VOLUME_KIND_IMAGE');
    await expect(w).toContainText('image volume flow (/data/flows.json) is a Folder in image node-red:v1');
    await expect(w).toContainText('pick a folder volume here, or set image volume flow to File on the image page');
    await expect(page.getByTestId('vol-flow-imgkind')).toHaveText('Folder');
    await expect(page.getByTestId('vol-flow-fix-image')).toContainText('Set image volume flow to File on the image page');
    await expect(page.getByTestId('vol-flow-fix-volume')).toContainText('New folder volume');
    await page.locator('#sec-volumes').scrollIntoViewIfNeeded();
    await page.waitForTimeout(300);
    await page.locator('#sec-volumes').screenshot({ path: `${SHOTS}/volkind-warning.png` });
    await page.getByTestId('vol-flow-fix-image').click();
    await page.waitForURL('**/image/v1?volume=flow**');
    await expect(page.getByTestId('im-fix-volume')).toContainText('Fixing image volume flow');
    await expect(volRow(page, 'flow')).toHaveClass(/focus/);
    await volRow(page, 'flow').getByTestId('vol-kind-file').click();
    await page.waitForTimeout(300);
    await page.screenshot({ path: `${SHOTS}/volkind-image-page-fix.png` });
    await page.getByTestId('im-save').click();
    await expectToast(page, 'success', 'Image saved');
    await page.getByTestId('im-fix-back').click();
    await page.waitForURL('**/containers/node-red');
    await expect(page.getByTestId('vol-flow-imgkind')).toHaveText('File');
    await expect(issue(page, 'flow', 'V_VOLUME_KIND_IMAGE')).toHaveCount(0);
    await expect(issue(page, 'flow', 'V_FILE_VOLUME_RENAME')).toHaveCount(1);
  });

  test('a disabled container with a folder volume on the File image volume shows the error too', async ({ page }) => {
    await login(page, 'admin', C2);
    await expect(issue(page, 'flow', 'V_VOLUME_KIND_MISMATCH')).toBeVisible();
  });
});
