// §21 #105 one action: Commit. GUI edits stay in this browser (localStorage per site branch, typed secret values never)
// until Commit — the agent applies them and writes the commit only when the apply succeeds. "Uncommitted" on the agent =
// changes made on the server only (drift), committed first. Screenshots (OPM_J_SHOTS=1):
// screenshots/j-{changebar,commit,drift-first,restore}-{light,dark}.png (390 px: commit-only-105-mobile.spec.ts).
import type { Page } from '@playwright/test';
import { test, expect } from './fixtures';
import { knobs, login, open, shot, pickOption, idbDrafts, nav } from './helpers';

const SHOTS = process.env.OPM_J_SHOTS === '1';
const snap = async (page: Page, name: string, full = false) => {
  if (SHOTS) await shot(page, name, full);
};
const bar = (page: Page) => page.getByTestId('local-edit-bar');

test('no Save button on any config page: one change bar with Discard · Review · Commit…', async ({ page }) => {
  await knobs(page, { 'opm.mock.branch.clean': '1' });
  await login(page, '/_opm/app/app/variables');
  const pages = ['/_opm/app/app/variables', '/_opm/app/app/secrets', '/_opm/app/app/files', '/_opm/app/app/config', '/_opm/app/app/config/edge', '/_opm/app/app/config/volumes', '/_opm/app/app/config/settings', '/_opm/app/app/config/containers/hello-api', '/_opm/app/app/config/edge/route'];
  for (const p of pages) {
    if (p !== pages[0]) await open(page, p);
    await expect(bar(page), p).toBeVisible();
    await expect(page.getByTestId('save-working'), p).toHaveCount(0);
    await expect(page.getByRole('button', { name: /^Save\b/ }), p).toHaveCount(0);
    await expect(page.getByTestId('change-bar-commit'), p).toBeVisible();
  }
});

test('several pages → ONE commit; a reload restores the set (secret values are never stored)', async ({ page }) => {
  await knobs(page, { 'opm.mock.branch.clean': '1' });
  await login(page, '/_opm/app/app/variables');
  await pickOption(page, 'var-theme', 'blue');
  await expect(bar(page)).toContainText('1 change (not committed)');
  await page.getByTestId('tree-app-files').click(); // §21 #126: Files of site app (Shared files is under Server)
  await page.getByTestId('edit-banner').click();
  await page.getByTestId('file-drawer').getByTestId('code-editor').fill('Welcome to the plant, night shift\n');
  await page.getByTestId('file-done').click();
  await expect(bar(page)).toContainText('2 changes (not committed)');
  await page.getByRole('link', { name: 'Secrets' }).click();
  await page.getByTestId('new-api-token').fill('a-new-token-value');
  await expect(bar(page)).toContainText('3 changes (not committed)');
  await snap(page, 'j-changebar-light');
  // §21 #107 auto-saved as a local draft (IndexedDB) without the typed secret value
  await expect.poll(async () => (await idbDrafts(page)).map((d) => d.count)).toEqual([3]);
  const stored = (await idbDrafts(page)).map((d) => d.json);
  expect(stored[0]).not.toContain('a-new-token-value');
  expect(stored[0]).toContain('"droppedValues":["api-token"]');
  expect(await page.evaluate(() => Object.keys(localStorage).filter((k) => k.startsWith('opm.pending.')).length)).toBe(0);

  // reload (the mock agent restarts with the same head): the set is offered, the secret value must be re-entered
  await page.reload();
  await page.getByTestId('actor-email').fill('tester@acme.example');
  await page.getByTestId('actor-continue').click();
  const banner = page.getByTestId('pending-banner');
  await expect(banner).toHaveAttribute('data-state', 'same');
  await expect(banner).toContainText('unsaved changes');
  await expect(banner).toContainText('3 changes');
  await expect(page.getByTestId('pending-banner-dropped')).toContainText('api-token');
  await snap(page, 'j-restore-light');
  await page.getByTestId('pending-banner-restore').click();
  await expect(banner).toHaveCount(0);
  await expect(bar(page)).toContainText('2 changes (not committed)');

  // ONE commit of both pages
  await page.getByTestId('change-bar-commit').click();
  const dlg = page.getByTestId('local-commit-dialog');
  await expect(dlg.getByTestId('commit-changes-toggle')).toContainText('2 changes');
  await dlg.getByTestId('commit-message').fill('Night shift: blue theme + banner');
  await snap(page, 'j-commit-light');
  await dlg.getByTestId('commit-submit').click();
  await expect(page).toHaveURL(/\/branch$/);
  await expect(page.getByTestId('update-result')).toContainText('Applied', { timeout: 40_000 });
  await page.getByTestId('branch-tabs-history').click();
  await expect(page.getByTestId('history-row-8')).toContainText('Night shift: blue theme + banner', { timeout: 10_000 });
  await expect(page.getByTestId('history-row-9')).toHaveCount(0);
  await expect.poll(async () => (await idbDrafts(page)).length).toBe(0);
});

test('drift first: the commit is refused until the server changes are committed; then the set is rebased and committed', async ({ page }) => {
  await login(page, '/_opm/app/app/variables');
  await pickOption(page, 'var-theme', 'blue');
  await page.getByTestId('change-bar-commit').click();
  const dlg = page.getByTestId('local-commit-dialog');
  await expect(dlg.getByTestId('drift-first')).toContainText('must be committed first');
  await expect(dlg.getByTestId('commit-submit')).toBeDisabled();
  await snap(page, 'j-drift-first-light');
  await dlg.getByTestId('drift-commit-open').click();
  const dd = page.getByTestId('drift-commit-dialog');
  await dd.getByTestId('drift-accept-notes').check();
  await dd.getByTestId('commit-message').fill('Record adminer + the cert edited by hand');
  await dd.getByTestId('commit-submit').click();
  await expect(dlg.getByTestId('commit-info')).toContainText('rebased', { timeout: 10_000 });
  await expect(dlg.getByTestId('drift-first')).toHaveCount(0);
  await dlg.getByTestId('commit-message').fill('Theme blue');
  await dlg.getByTestId('commit-submit').click();
  await expect(page.getByTestId('update-result')).toContainText('Applied', { timeout: 40_000 });
  await page.getByTestId('branch-tabs-history').click();
  await expect(page.getByTestId('history-row-8')).toContainText('Record adminer', { timeout: 10_000 });
  await expect(page.getByTestId('history-row-9')).toContainText('Theme blue', { timeout: 10_000 });
});

test('a failed apply writes no commit; the changes stay in the browser', async ({ page }) => {
  await knobs(page, { 'opm.mock.branch.clean': '1', 'opm.mock.branch.failApply': '1' });
  await login(page, '/_opm/app/app/variables');
  await pickOption(page, 'var-theme', 'blue');
  await page.getByTestId('change-bar-commit').click();
  const dlg = page.getByTestId('local-commit-dialog');
  await dlg.getByTestId('commit-message').fill('This one fails');
  await dlg.getByTestId('commit-submit').click();
  await expect(page.getByTestId('update-result')).not.toContainText('Applied', { timeout: 40_000 });
  await expect(page.getByText('Nothing was committed')).toBeVisible({ timeout: 40_000 });
  await page.getByTestId('branch-tabs-history').click();
  await expect(page.getByTestId('history-row-7')).toBeVisible();
  await expect(page.getByTestId('history-row-8')).toHaveCount(0);
  await page.getByTestId('branch-tabs-overview').click();
  await expect(page.getByTestId('n-edits')).toHaveText('1');
});

test('the head moves under pending changes in this tab → rebased onto it (a conflict asks)', async ({ page }) => {
  await login(page, '/_opm/app/app/files');
  // a conflicting edit: the per-site copy of client-cert was also edited on the server (drift)
  await page.getByTestId('edit-client-cert').click();
  await page.getByTestId('file-drawer').getByTestId('code-editor').fill('-----BEGIN CERTIFICATE-----\nMINE\n-----END CERTIFICATE-----\n');
  await page.getByTestId('file-done').click();
  await page.getByRole('link', { name: 'Variables' }).click();
  await pickOption(page, 'var-theme', 'blue');
  await expect(bar(page)).toContainText('2 changes (not committed)');
  // someone records the server changes (drift commit) → the head moves
  await nav(page, 'branch');
  await page.getByTestId('branch-tabs-uncommitted').click();
  await page.getByTestId('note-accept-file:api-config:sha256:9a1f').check();
  await page.getByTestId('commit-selected').click();
  await page.getByTestId('commit-dialog').getByTestId('commit-message').fill('Server changes');
  await page.getByTestId('commit-dialog').getByTestId('commit-submit').click();
  // §21 #107: no stale draft offer in-session — the open tab rebases its changes onto the new head
  const rd = page.getByTestId('rebase-dialog');
  await expect(rd).toBeVisible({ timeout: 20_000 });
  await expect(rd.getByTestId('rebase-item-files/client-cert')).toBeVisible();
  await rd.getByTestId('rebase-resolve-files/client-cert').getByText('Take mine').click();
  await rd.getByTestId('rebase-dialog-apply').click();
  await expect(page.getByTestId('pending-banner')).toHaveCount(0);
  await page.getByTestId('branch-tabs-overview').click();
  await expect(page.getByTestId('n-edits')).toHaveText('2');
  // the rebased set kept my cert and my theme on top of the new head
  await page.getByTestId('tree-app-variables').click();
  await expect(bar(page)).toContainText('2 changes (not committed)');
});

for (const scheme of ['dark'] as const) {
  test(`screens · ${scheme}`, async ({ page }) => {
    test.skip(!SHOTS, 'screenshots only (OPM_J_SHOTS=1)');
    await page.emulateMedia({ colorScheme: scheme });
    await login(page, '/_opm/app/app/variables');
    await pickOption(page, 'var-theme', 'blue');
    await page.getByTestId('new-api-token').count();
    await shot(page, `j-changebar-${scheme}`, false);
    await page.getByTestId('change-bar-commit').click();
    await expect(page.getByTestId('local-commit-dialog').getByTestId('drift-first')).toBeVisible();
    await shot(page, `j-drift-first-${scheme}`, false);
    await page.getByTestId('local-commit-dialog').getByTestId('commit-cancel').click();
    await knobs(page, { 'opm.mock.branch.clean': '1' });
    await page.reload();
    await page.getByTestId('actor-email').fill('tester@acme.example');
    await page.getByTestId('actor-continue').click();
    await expect(page.getByTestId('pending-banner')).toBeVisible();
    await shot(page, `j-restore-${scheme}`, false);
    await page.getByTestId('pending-banner-restore').click();
    await page.getByTestId('change-bar-commit').click();
    await page.getByTestId('local-commit-dialog').getByTestId('commit-message').fill('Theme blue for the control room');
    await shot(page, `j-commit-${scheme}`, false);
  });
}
