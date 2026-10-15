// SPEC-final §21 #96 — the volume manager in Tools → Volumes (mock agent): overview with live users, file operations
// (new folder, new file + edit with a diff preview, upload, rename, delete), Copy (path / another volume), Copies →
// Restore, Empty (wait + typed name, snapshot first), Restore from an uploaded .tar.gz, History, failures, "*", and the
// data-directory mismatch helper on the Status page.
import type { Page } from '@playwright/test';
import { test, expect } from './fixtures';
import { knobs, login, shot } from './helpers';

const VOLS = '/_opm/app/app/volumes';

async function waitOp(page: Page) {
  const d = page.getByTestId('volop-drawer');
  await expect(d).toBeVisible();
  await expect(d.getByTestId('volop-result')).toBeVisible({ timeout: 20_000 });
  return d;
}
async function closeOp(page: Page) {
  await page.getByTestId('volop-close').click();
  await expect(page.getByTestId('volop-drawer')).toBeHidden();
}

test.beforeEach(async ({ page }) => {
  await knobs(page, { 'opm.mock.waitSec': '2' });
});

test('overview: size, kind, location, users with their live state, starter content', async ({ page }) => {
  await login(page, `${VOLS}?v=api-env`);
  const ov = page.getByTestId('vol-overview');
  await expect(ov).toBeVisible();
  await expect(ov.getByTestId('ov-size')).not.toHaveText('—');
  await expect(ov).toContainText('/var/lib/opm/volumes/api-env');
  await expect(ov.getByTestId('vol-users')).toContainText('hello-api');
  await expect(ov.getByTestId('user-state-hello-api')).toHaveText(/running|exited|restarting/);
  await expect(ov).toContainText('(legacy) from Files api-config');
  await shot(page, 'vm-overview');
});

test('files: new folder, new file, edit with a diff preview, upload, rename, delete (typed DELETE)', async ({ page }) => {
  await login(page, `${VOLS}?v=api-env&tab=files`);
  await expect(page.getByTestId('vol-entries')).toContainText('worker.ts');
  // new folder
  await page.getByTestId('new-folder').click();
  await page.getByTestId('name-input').fill('/conf');
  await page.getByTestId('name-go').click();
  await expect(page.getByTestId('entry-conf')).toBeVisible();
  // new file
  await page.getByTestId('new-file').click();
  const ed = page.getByTestId('file-editor');
  await ed.getByTestId('new-file-name').fill('app.env');
  await ed.locator('textarea').fill('A=1\nB=2\n');
  await ed.getByTestId('file-review').click();
  await expect(ed.getByTestId('file-diff')).toContainText('A=1');
  await ed.getByTestId('file-save').click();
  await expect(page.getByTestId('entry-app.env')).toBeVisible();
  // edit an existing file: the diff shows the changed line
  await page.getByTestId('edit-worker.ts').click();
  await expect(ed.locator('textarea')).toHaveValue(/concurrency: 4/);
  await ed.locator('textarea').fill((await ed.locator('textarea').inputValue()).replace('concurrency: 4', 'concurrency: 8'));
  await ed.getByTestId('file-review').click();
  await expect(ed.getByTestId('file-diff').locator('.dl.del')).toContainText('concurrency: 4');
  await expect(ed.getByTestId('file-diff').locator('.dl.add')).toContainText('concurrency: 8');
  await shot(page, 'vm-edit-diff', false);
  await ed.getByTestId('file-save').click();
  await expect(ed).toBeHidden();
  // upload two files
  await page.getByTestId('up-files-input').setInputFiles([
    { name: 'notes.txt', mimeType: 'text/plain', buffer: Buffer.from('hello') },
    { name: 'blob.bin', mimeType: 'application/octet-stream', buffer: Buffer.alloc(2048) },
  ]);
  await expect(page.getByTestId('entry-notes.txt')).toBeVisible();
  await expect(page.getByTestId('entry-blob.bin')).toBeVisible();
  // the same again → "exists" → overwrite
  await page.getByTestId('up-files-input').setInputFiles([{ name: 'notes.txt', mimeType: 'text/plain', buffer: Buffer.from('again') }]);
  await page.getByTestId('exists-confirm').getByRole('button', { name: 'Overwrite them' }).click();
  await expect(page.getByTestId('uploads')).toContainText('done');
  // rename
  await page.getByTestId('mv-notes.txt').click();
  await page.getByTestId('name-input').fill('/conf/notes.txt');
  await page.getByTestId('name-go').click();
  await expect(page.getByTestId('entry-notes.txt')).toHaveCount(0);
  // delete two selected entries (typed DELETE)
  await page.getByTestId('sel-blob.bin').check();
  await page.getByTestId('sel-app.env').check();
  await page.getByTestId('del-selected').click();
  const conf = page.getByTestId('del-confirm');
  await expect(conf.getByRole('button', { name: 'Delete' })).toBeDisabled();
  await conf.getByRole('textbox').fill('DELETE');
  await conf.getByRole('button', { name: 'Delete' }).click();
  await expect(page.getByTestId('entry-blob.bin')).toHaveCount(0);
  await expect(page.getByTestId('entry-app.env')).toHaveCount(0);
  await shot(page, 'vm-files');
});

test('copy to a path → Copies → restore from it (wait + typed name, snapshot first); history', async ({ page }) => {
  await login(page, `${VOLS}?v=hello-api-data`);
  await page.getByTestId('vol-copy').click();
  const cd = page.getByTestId('copy-drawer');
  await expect(cd.getByTestId('copy-path')).toHaveAttribute('placeholder', /\/var\/lib\/opm\/volume-copies\/hello-api-data-/);
  // refused paths stay on the field
  await cd.getByTestId('copy-path').fill('/etc/backup');
  await expect(cd.getByTestId('copy-path-err')).toContainText('/etc');
  await expect(cd.getByTestId('copy-go')).toBeDisabled();
  await cd.getByTestId('copy-path').fill('');
  await shot(page, 'vm-copy', false);
  await cd.getByTestId('copy-go').click();
  const d = await waitOp(page);
  await expect(d.getByTestId('volop-result')).toContainText('copied to');
  await expect(d.getByTestId('volop-step-copy')).toHaveAttribute('data-state', 'done');
  await shot(page, 'vm-copy-done', false);
  await closeOp(page);
  await page.getByTestId('vtab-copies').click();
  const copies = page.getByTestId('vol-copies');
  await expect(copies.locator('tr[data-testid^="copy-"]')).toHaveCount(2); // the seeded copy + this one
  await copies.locator('[data-testid^="restore-from-"]').first().click();
  const rf = page.getByTestId('restore-flow');
  await expect(rf.getByTestId('countdown')).toBeVisible();
  await expect(rf.getByTestId('ff-hold')).toBeVisible(); // the shared countdown with fast-forward
  await expect(rf.getByTestId('accept-input')).toBeVisible({ timeout: 8000 });
  await rf.getByTestId('accept-input').fill('ACCEPT');
  await expect(rf.getByTestId('restore-go')).toBeDisabled();
  await rf.getByTestId('accept-input').fill('hello-api-data');
  await rf.getByTestId('restore-go').click();
  const d2 = await waitOp(page);
  await expect(d2.getByTestId('volop-snapshot')).toBeVisible();
  await expect(d2.getByTestId('volop-step-stop')).toContainText('hello-api');
  await closeOp(page);
  await page.getByTestId('vtab-history').click();
  await expect(page.getByTestId('vol-history')).toContainText('Restore');
  await expect(page.getByTestId('vol-history')).toContainText('Copy');
  await shot(page, 'vm-history');
});

test('copy into another volume: empty target directly, non-empty only with its name typed', async ({ page }) => {
  await login(page, `${VOLS}?v=api-env`);
  await page.getByTestId('vol-copy').click();
  const cd = page.getByTestId('copy-drawer');
  await cd.getByTestId('copy-mode').getByRole('radio', { name: 'Another volume' }).click();
  await cd.getByTestId('copy-target').click();
  await page.getByRole('option', { name: /hello-api-2-data/ }).click();
  await cd.getByTestId('copy-go').click();
  await waitOp(page);
  await closeOp(page);
  // again: now the target has data → overwrite needs its name
  await page.getByTestId('vol-copy').click();
  await cd.getByTestId('copy-mode').getByRole('radio', { name: 'Another volume' }).click();
  await cd.getByTestId('copy-target').click();
  await page.getByRole('option', { name: /hello-api-2-data/ }).click();
  await expect(cd).toContainText('is not empty');
  await expect(cd.getByTestId('copy-go')).toBeDisabled();
  await cd.getByTestId('copy-overwrite').fill('hello-api-2-data');
  await cd.getByTestId('copy-go').click();
  const d = await waitOp(page);
  await expect(d.getByTestId('volop-step-empty')).toHaveAttribute('data-state', 'done');
});

test('empty: wait + typed volume name, snapshot first; the volume shows 0 B; while running the volume is busy', async ({ page }) => {
  await login(page, `${VOLS}?v=hello-api-data`);
  await page.getByTestId('vol-empty').click();
  const ef = page.getByTestId('empty-flow');
  await expect(ef.getByTestId('countdown')).toBeVisible();
  await expect(ef.getByTestId('empty-snapshot')).toBeChecked();
  await expect(ef.getByTestId('accept-input')).toBeVisible({ timeout: 8000 });
  await ef.getByTestId('accept-input').fill('hello-api-data');
  await shot(page, 'vm-empty', false);
  await ef.getByTestId('empty-go').click();
  await expect(page.getByTestId('volop-drawer')).toBeVisible();
  await page.getByTestId('volop-close').click();
  await expect(page.getByTestId('vol-busy')).toBeVisible();
  await expect(page.getByTestId('vol-empty')).toBeDisabled();
  await page.getByTestId('vol-busy').getByRole('button', { name: 'View progress' }).click();
  const d = await waitOp(page);
  await expect(d.getByTestId('volop-result')).toContainText('Empty finished');
  await expect(d.getByTestId('volop-snapshot')).toBeVisible();
  await closeOp(page);
  await expect(page.getByTestId('vol-hello-api-data')).toContainText('0 B');
});

test('restore from an uploaded .tar.gz; a failing action reports the step and starts the containers again', async ({ page }) => {
  await login(page, `${VOLS}?v=hello-api-data`);
  await page.getByTestId('vol-restore').click();
  const pick = page.getByTestId('restore-pick');
  await pick.getByTestId('restore-file').setInputFiles({ name: 'backup.tar.gz', mimeType: 'application/gzip', buffer: Buffer.alloc(4096, 1) });
  await pick.getByTestId('restore-upload').click();
  const rf = page.getByTestId('restore-flow');
  await expect(rf.getByTestId('restore-source')).toContainText('backup.tar.gz');
  await expect(rf.getByTestId('accept-input')).toBeVisible({ timeout: 8000 });
  await rf.getByTestId('accept-input').fill('hello-api-data');
  await rf.getByTestId('restore-go').click();
  await waitOp(page);
  await closeOp(page);
  // failure
  await page.evaluate(() => localStorage.setItem('opm.mock.volFail', 'copy'));
  await page.getByTestId('vol-copy').click();
  await page.getByTestId('copy-drawer').getByTestId('copy-go').click();
  const d = await waitOp(page);
  await expect(d.getByTestId('volop-result')).toContainText('Copy failed');
  await expect(d.getByTestId('volop-step-copy')).toHaveAttribute('data-state', 'failed');
});

test('"*": every site\'s volumes with Manage links', async ({ page }) => {
  await login(page, '/_opm/app/*/volumes');
  const t = page.getByTestId('all-volumes');
  await expect(t).toContainText('redis-data');
  await expect(t).toContainText('nodered-data');
  await t.getByTestId('manage-db-redis-data').click();
  await expect(page).toHaveURL(/\/app\/db\/volumes\?v=redis-data/);
  await expect(page.getByTestId('vol-panel')).toContainText('redis-data');
});

test('data-directory mismatch on Status → copy the data volume aside, then empty it', async ({ page }) => {
  await knobs(page, { 'opm.mock.pgMismatch': 'app/hello-api' });
  await login(page, '/_opm/app/app/status');
  const hint = page.getByTestId('mismatch-hello-api');
  await expect(hint).toContainText('DATA DIRECTORY / IMAGE VERSION MISMATCH');
  await shot(page, 'vm-mismatch', false);
  await hint.getByTestId('mismatch-fix-hello-api').click();
  const dr = page.getByTestId('aside-drawer');
  await dr.getByTestId('aside-copy').click();
  await expect(dr.getByTestId('aside-copy-path')).toContainText('/var/lib/opm/volume-copies/', { timeout: 15_000 });
  await dr.getByTestId('aside-empty').click();
  const ef = page.getByTestId('empty-flow');
  await expect(ef.getByTestId('empty-snapshot')).not.toBeChecked(); // a copy exists already
  await expect(ef.getByTestId('accept-input')).toBeVisible({ timeout: 8000 });
  const name = await ef.locator('label[for="accept-in"] .mono').innerText();
  await ef.getByTestId('accept-input').fill(name);
  await ef.getByTestId('empty-go').click();
  await expect(dr.getByTestId('aside-done')).toBeVisible({ timeout: 15_000 });
  await shot(page, 'vm-mismatch-done', false);
});
