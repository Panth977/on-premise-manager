import { test, expect, type Page } from '@playwright/test';
import { login, expectToast, SEED, pickCombo, pickSource, commitAll, nav } from './helpers';

/**
 * Round D site volumes: the Volumes page of a site (declare / edit / back up / delete — blocked while used), the
 * container page Volumes section (one grouped dropdown, "+ New data volume…", read-write by default, file / secret
 * read-only badge, Edit path) and the validation codes V_VOLUME_UNDECLARED / V_VOLUME_UNUSED / V_VOLUME_NOT_MOUNTED /
 * V_READONLY.
 */
const TPL = `/app/templates/${SEED.template}`;
const CORE = '/app/templates/TplNorthwindCore0002';
const SHOTS = 'screenshots';

/** edit the persisted mock DB (simulates documents the UI cannot produce), then reload */
async function patchMockDb(page: Page, fn: string) {
  await page.waitForTimeout(1200); // the mock persists with a debounce
  await page.evaluate((src) => {
    const db = JSON.parse(localStorage.getItem('opm.mock.db')!);
    new Function('db', src)(db);
    localStorage.setItem('opm.mock.db', JSON.stringify(db));
  }, fn);
  await page.reload();
}

test.describe('round D: site volumes', () => {
  test('Volumes page: declared volumes, used by, add with validation, backup toggle, edit, delete', async ({ page }) => {
    await login(page, 'admin', `${CORE}/sites/core/volumes`);
    await expect(page.getByTestId('volumes-info')).toContainText('Volumes are folders on the site server (auto: <agentHome>/volumes/<name>; ./ = the agent home). Back up = included in snapshots, wherever the folder is. Host paths are never backed up.');
    const shared = page.getByTestId('volume-row-core-api-data');
    await expect(shared).toContainText('Core API data');
    await expect(shared.getByTestId('volume-users-core-api-data').getByTestId('volume-user')).toHaveText(['core-api', 'core-worker']);
    await expect(page.getByTestId('vol-backup-cache-data').locator('input')).not.toBeChecked();
    await page.screenshot({ path: `${SHOTS}/d-mock-volumes-page.png` });

    // add: name validation (format, duplicate), backup on by default
    await page.getByTestId('add-volume').click();
    const m = page.getByTestId('volume-modal');
    await m.getByTestId('vm-slug').fill('Bad Name!');
    await expect(m).toContainText('starting with a letter or digit');
    await m.getByTestId('vm-slug').fill('cache-data');
    await expect(m).toContainText('site core already has a volume cache-data');
    await expect(m.getByTestId('vm-save')).toBeDisabled();
    await m.getByTestId('vm-slug').fill('uploads');
    await m.getByTestId('vm-label').fill('User uploads');
    await expect(m.getByTestId('vm-backup').locator('input')).toBeChecked();
    await m.getByTestId('vm-save').click();
    await expect(m).toHaveCount(0);
    const row = page.getByTestId('volume-row-uploads');
    await expect(row).toContainText('User uploads');
    await expect(row).toContainText('unused');
    // declared but mounted by nobody → V_VOLUME_UNUSED (warning, still saveable)
    await expect(page.getByTestId('volume-issue-uploads')).toContainText('not mounted by any container');
    await expect(page.getByTestId('site-issues')).toContainText('V_VOLUME_UNUSED');
    await expect(page.getByTestId('tree-core-volumes').locator('.nav-cnt.amber')).toBeVisible();
    // backup toggle + edit (name fixed)
    await page.getByTestId('vol-backup-uploads').click();
    await expect(page.getByTestId('vol-backup-uploads').locator('input')).not.toBeChecked();
    await page.getByTestId('vol-edit-uploads').click();
    await expect(m.getByTestId('vm-slug')).toBeDisabled();
    await m.getByTestId('vm-desc').fill('Files users upload');
    await m.getByTestId('vm-save').click();
    await expect(row).toContainText('Files users upload');
    await commitAll(page);
    await expect(page.getByTestId('template-version')).toHaveText('v3');

    // delete of a used volume is blocked and lists the containers
    await page.getByTestId('vol-delete-core-api-data').click();
    const blocked = page.getByTestId('volume-in-use');
    await expect(blocked).toBeVisible();
    await expect(blocked.getByTestId('volume-in-use-list')).toContainText('core-api');
    await expect(blocked.getByTestId('volume-in-use-list')).toContainText('core-worker');
    await page.waitForTimeout(350); // overlay transition
    await page.screenshot({ path: `${SHOTS}/d-mock-delete-blocked.png` });
    await blocked.getByTestId('volume-in-use-close').click();
    await expect(page.getByTestId('volume-row-core-api-data')).toBeVisible();
    // an unused one can be deleted
    await page.getByTestId('vol-delete-uploads').click();
    await page.getByTestId('delete-volume').getByRole('button', { name: 'Delete volume' }).click();
    await expect(page.getByTestId('volume-row-uploads')).toHaveCount(0);
  });

  test('container page: one grouped dropdown, shared with, read-write by default, file badge, Edit path', async ({ page }) => {
    await login(page, 'admin', `${CORE}/sites/core/containers/core-worker`);
    // the data volume shared with core-api, read-write
    await expect(page.getByTestId('vol-data-source')).toHaveAttribute('data-value', 'volume:core-api-data');
    await expect(page.getByTestId('vol-data-shared')).toHaveText('shared · 2 containers'); // shared form: badge + list (§21 #102)
    await expect(page.getByTestId('vol-data-shared-list')).toContainText('core-api');
    await expect(page.getByTestId('vol-data-ro').locator('input')).not.toBeChecked();
    await expect(page.getByTestId('vol-data-ro')).toContainText('read-write');
    // no segmented control: one listbox with groups
    await expect(page.getByTestId('vol-data-kind')).toHaveCount(0);
    await page.getByTestId('vol-data-source').click();
    const list = page.getByTestId('vol-data-source-list');
    await expect(list.locator('[role="group"]')).toHaveCount(3); // data volumes · secrets · advanced (host path, not mounted) — no files
    await expect(list.locator('[data-value="volume:core-api-data"]')).toContainText('shared with core-api');
    await expect(list.locator('[data-value="volume:cache-data"]')).toContainText('no backup');
    await expect(list.locator('[data-value="new-volume"]')).toHaveText('+ New data volume…');
    await page.waitForTimeout(350); // overlay transition
    await page.screenshot({ path: `${SHOTS}/d-mock-container-volumes.png` });
    await page.keyboard.press('Escape');
    // a secret → read-only badge instead of the toggle; back to a volume → read-write again (the round-C bug)
    await pickSource(page, 'vol-data-source', 'secret:db-password');
    await expect(page.getByTestId('vol-data-ro-badge')).toContainText('read-only');
    await expect(page.getByTestId('vol-data-ro')).toHaveCount(0);
    await pickSource(page, 'vol-data-source', 'volume:core-api-data');
    await expect(page.getByTestId('vol-data-ro').locator('input')).not.toBeChecked();
    // a host path is read-write by default too, never backed up
    await pickSource(page, 'vol-data-source', 'host');
    await page.getByTestId('vol-data-host').fill('/srv/core/data');
    await expect(page.getByTestId('vol-data-ro').locator('input')).not.toBeChecked();
    await expect(page.getByTestId('sum-mounts')).toContainText('host /srv/core/data');
    await page.getByTestId('vol-data-ro').click();
    await expect(page.getByTestId('sum-mounts')).toContainText('· ro');
    await pickSource(page, 'vol-data-source', 'volume:core-api-data');
    // Edit path → expression input, Reset → the image path
    await expect(page.getByTestId('vol-data-path')).toHaveText('/data');
    await page.getByTestId('vol-data-edit-path').click();
    await page.getByTestId('vol-data-mount-input').fill('/var/lib/core');
    await expect(page.getByTestId('sum-mounts')).toContainText('/var/lib/core');
    await page.getByTestId('vol-data-reset-path').click();
    await expect(page.getByTestId('vol-data-path')).toHaveText('/data');
    await page.getByTestId('discard-container').click();
    await expect(page.getByTestId('vol-data-ro').locator('input')).not.toBeChecked();
  });

  test('"+ New data volume…" from the container page declares the volume on the site and selects it', async ({ page }) => {
    await login(page, 'admin', `${CORE}/sites/core/containers/core-api`);
    await expect(page.getByTestId('vol-config-source')).toHaveAttribute('data-value', 'none');
    await pickSource(page, 'vol-config-source', 'new-volume');
    const m = page.getByTestId('volume-modal');
    await expect(m).toContainText('for core-api · config');
    await expect(m.getByTestId('vm-slug')).toHaveValue('core-api-config');
    await m.getByTestId('vm-desc').fill('env config files');
    await page.waitForTimeout(350); // overlay transition
    await page.screenshot({ path: `${SHOTS}/d-mock-new-volume.png` });
    await m.getByTestId('vm-save').click();
    await expect(page.getByTestId('vol-config-source')).toHaveAttribute('data-value', 'volume:core-api-config');
    await expect(page.getByTestId('vol-config-ro').locator('input')).not.toBeChecked();
    await expect(page.getByTestId('vol-config-path')).toHaveText('/app/helper/config/env/integration.ts');
    // the working copy is shared with the Volumes page (no prompt inside the site)
    await page.getByTestId('site-volumes-link').click();
    await page.waitForURL(`**${CORE}/sites/core/volumes`);
    await expect(page.getByTestId('leave-modal')).toHaveCount(0);
    await expect(page.getByTestId('volume-row-core-api-config')).toContainText('env config files');
    await expect(page.getByTestId('volume-users-core-api-config')).toContainText('core-api');
    await commitAll(page);
  });

  test('a new container: image volumes start not mounted (warning), Cancel takes back a volume declared on the page', async ({ page }) => {
    await login(page, 'admin', `${CORE}/sites/core/containers`);
    await page.getByTestId('add-container').click();
    await pickCombo(page, 'c-service', SEED.svcRedis, 'red');
    await page.getByTestId('c-name').fill('cache-2');
    await expect(page.getByTestId('vol-data-source')).toContainText('Not mounted — pick a data volume');
    await expect(page.getByTestId('vol-row-data').locator('[data-code="V_VOLUME_NOT_MOUNTED"]')).toBeVisible();
    await expect(page.getByTestId('container-issues')).toContainText('V_VOLUME_NOT_MOUNTED');
    await expect(page.getByTestId('apply-container')).toBeEnabled(); // a warning
    await pickSource(page, 'vol-data-source', 'new-volume');
    await expect(page.getByTestId('vm-slug')).toHaveValue('cache-2-data');
    await page.getByTestId('vm-save').click();
    await expect(page.getByTestId('vol-row-data').locator('[data-code="V_VOLUME_NOT_MOUNTED"]')).toHaveCount(0);
    await page.getByTestId('cancel-container').click();
    await page.waitForURL(`**${CORE}/sites/core/containers`);
    await page.getByTestId('tree-core-volumes').click();
    await expect(page.getByTestId('volume-row-cache-2-data')).toHaveCount(0);
    await expect(page.getByTestId('dirty-dot')).toHaveCount(0);
  });

  test('validation codes: V_VOLUME_UNDECLARED (Declare it); a file mount stored writable is read as read-only', async ({ page }) => {
    await login(page, 'admin', `${TPL}/sites/app/containers/hello-web`);
    // documents the UI cannot produce: hello-api mounts an undeclared volume, the banner file mount is stored writable
    await patchMockDb(
      page,
      `const t = db.templates['${SEED.template}'];
       const app = t.sites.find((s) => s.siteSlug === 'app');
       app.config.volumes = app.config.volumes.filter((v) => v.volumeSlug !== 'hello-api-data');
       app.config.containers.find((c) => c.containerSlug === 'hello-web').volumes[0].readOnly = false;`,
    );
    // shared normalizeContainer forces file / secret mounts read-only on read (V_READONLY cannot reach the editor)
    await expect(page.getByTestId('extra-0-ro-badge')).toHaveText('read-only');
    await expect(page.getByTestId('extra-0-ro')).toHaveCount(0);
    await expect(page.getByTestId('container-issues')).not.toContainText('V_READONLY');

    await nav(page, `${TPL}/sites/app/containers/hello-api`);
    await expect(page.getByTestId('vol-data-issue')).toHaveAttribute('data-code', 'V_VOLUME_UNDECLARED');
    await expect(page.getByTestId('container-issues')).toContainText('V_VOLUME_UNDECLARED');
    await expect(page.getByTestId('vol-data-source')).toContainText('hello-api-data');
    await expect(page.getByTestId('tree-badge-app')).toHaveClass(/red/);
    await page.getByTestId('vol-data-declare').click();
    await expect(page.getByTestId('vol-data-issue')).toHaveCount(0);
    await expect(page.getByTestId('apply-container')).toBeEnabled();
    await page.getByTestId('apply-container').click();
    await expectToast(page, 'success', /applied|added/);
  });

  test('a document stored before round D opens normalized: data source → declared volume, read-write (round-C read-only reset)', async ({ page }) => {
    const LEG = '/app/templates/TplLegacyRoundC00003';
    await login(page, 'admin', `${LEG}/sites/old/containers/cache`);
    await expect(page.getByTestId('vol-data-source')).toHaveAttribute('data-value', 'volume:cache-data');
    await expect(page.getByTestId('vol-data-ro').locator('input')).not.toBeChecked();
    await expect(page.getByTestId('container-issues')).not.toContainText('V_VOLUME_UNDECLARED');
    await page.getByTestId('tree-old-volumes').click();
    await expect(page.getByTestId('volume-row-cache-data')).toBeVisible();
    await expect(page.getByTestId('vol-backup-cache-data').locator('input')).toBeChecked();
    await expect(page.getByTestId('volume-users-cache-data')).toContainText('cache');
  });
});
