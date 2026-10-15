import { test, expect } from '@playwright/test';
import { login, expectToast, SEED, pickIcon, pick } from './helpers';

const D = `/app/deployments/${SEED.deployment}`;
const E = `/app/deployments/${SEED.deploymentEmpty}`;

test.describe('deployment scope', () => {
  test('overview cards + edit deployment modal', async ({ page }) => {
    await login(page, 'admin', D);
    await expect(page.getByTestId('card-head')).toContainText('commit 2'); // §21 #105 migrate-j auto-committed the old working copy
    await expect(page.getByTestId('card-uncommitted')).toContainText('Everything is committed');
    await expect(page.getByTestId('card-agent-branches')).toContainText('3');
    await expect(page.getByTestId('card-sites')).toContainText('3 / 3'); // app, db + lab (§21 #90 mock: enrolled, not in the draft)
    await expect(page.getByTestId('overview-activities')).toBeVisible();
    await page.getByTestId('edit-deployment').click();
    const m = page.getByTestId('deployment-drawer');
    await m.getByTestId('dep-label').fill('');
    await m.getByTestId('dep-save').click();
    await expect(m.getByTestId('field-error').first()).toBeVisible();
    await m.getByTestId('dep-label').fill('ACME plant 2');
    // logo = IconPicker (stacked over the modal), tags / managers = pickers
    await pickIcon(page, 'dep-logo', 'factory');
    await expect(m.getByTestId('dep-tags')).toBeVisible();
    await expect(m.getByTestId('dep-managers')).toBeVisible();
    await m.getByTestId('dep-save').click();
    await expectToast(page, 'success', 'Details saved');
    await expect(page.getByTestId('dep-name')).toHaveText('ACME plant 2');
    await page.getByTestId('edit-deployment').click();
    await expect(page.getByTestId('dep-logo')).toBeVisible();
    await expect(page.getByTestId('dep-logo').locator('svg').first()).toBeVisible();
  });

  test('delete deployment needs the typed label', async ({ page }) => {
    await login(page, 'admin', E);
    await page.getByTestId('delete-deployment').click();
    const ok = page.getByTestId('confirm-ok');
    await expect(ok).toBeDisabled();
    await page.getByTestId('typed-confirm').fill('North');
    await expect(ok).toBeDisabled();
    await page.getByTestId('typed-confirm').fill('North grid');
    await ok.click();
    await page.waitForURL('**/app/deployments');
    await page.goto(E);
    await expect(page.getByText('Deployment not found or you cannot see it')).toBeVisible();
  });

  test('setup: install command (not stored → regenerate), revoke token, create, regenerate key', async ({ page }) => {
    await login(page, 'admin', `${E}/setup`);
    const site = page.getByTestId('setup-site-main');
    // the seeded active command was made before §21 #84 → not stored
    await expect(site.getByTestId('install-cmd-not-stored')).toHaveText('not stored — regenerate to view');
    await expect(site.getByTestId('create-install')).toHaveCount(0);
    await site.getByTestId('install-cmd-regenerate').click();
    await page.getByTestId('install-cmd-confirm').getByTestId('confirm-ok').click();
    await expect(site.getByTestId('install-cmd-value')).toContainText('Authorization: Bearer opi_');
    await expect(site.getByTestId('install-command')).toContainText('expires in');
    await expect(site.getByTestId('token-row')).toHaveCount(2);
    await expect(site.getByTestId('token-row').filter({ hasText: 'revoked' })).toHaveCount(1);
    await site.getByTestId('token-row').filter({ hasText: 'active' }).getByTestId('revoke-token').click();
    await page.getByTestId('confirm-ok').click();
    await expect(site.getByTestId('token-row').filter({ hasText: 'active' })).toHaveCount(0);
    await expect(site.getByTestId('install-command')).toHaveCount(0);
    await site.getByTestId('create-install').click();
    await expect(site.getByTestId('install-cmd-value')).toContainText('Authorization: Bearer opi_');

    // the key of North grid is stored: View, then Regenerate (typed ACCEPT)
    await page.getByTestId('access-key-view').click();
    await expect(page.getByTestId('access-key-value')).toHaveText(/^([A-Z2-7]{4}-){5}[A-Z2-7]{4}$/);
    const first = await page.getByTestId('access-key-value').textContent();
    await page.getByTestId('access-key-regenerate').click();
    const rot = page.getByTestId('access-key-confirm');
    await expect(rot).toContainText('Open sessions stay valid until their 6 h end.');
    await expect(rot.getByTestId('confirm-ok')).toBeDisabled();
    await rot.getByTestId('typed-confirm').fill('ACCEPT');
    await rot.getByTestId('confirm-ok').click();
    await expect(page.getByTestId('key-version')).toHaveText('v2');
    await expect(page.getByTestId('access-key-value')).toHaveText(/^([A-Z2-7]{4}-){5}[A-Z2-7]{4}$/);
    expect(await page.getByTestId('access-key-value').textContent()).not.toBe(first);
    await expect(page.getByTestId('access-key-modal')).toHaveCount(0);
  });

  test('setup: revoke enrollment needs the typed site slug', async ({ page }) => {
    await login(page, 'admin', `${D}/setup`);
    const db = page.getByTestId('setup-site-db');
    await expect(db.getByTestId('enrolled')).toBeVisible();
    await db.getByTestId('revoke-enrollment').click();
    await expect(page.getByTestId('confirm-ok')).toBeDisabled();
    await page.getByTestId('typed-confirm').fill('db');
    await page.getByTestId('confirm-ok').click();
    await expectToast(page, 'success', 'Enrollment of db revoked');
    await expect(db.getByTestId('create-install')).toBeVisible();
  });

  test('status table + uptime heatmaps', async ({ page }) => {
    await login(page, 'admin', `${D}/status`);
    await expect(page.getByTestId('status-row-app')).toContainText('online');
    await expect(page.getByTestId('heat30-app').locator('.cell')).toHaveCount(30);
    await expect(page.getByTestId('heat24-db').locator('.cell')).toHaveCount(24);
    await page.getByTestId('refresh-status').click();
    await expect(page.getByTestId('status-row-db')).toBeVisible();
  });

  test('activities: filter, load more, drawer', async ({ page }) => {
    await login(page, 'admin', `${D}/activities?limit=5`);
    await expect(page.getByTestId('activity-row')).toHaveCount(5);
    await page.getByTestId('load-more').click();
    await expect(page.getByTestId('activity-row')).toHaveCount(10);
    await pick(page, 'f-site', 'cloud');
    await expect(page).toHaveURL(/site=cloud/);
    await expect(page.getByTestId('activity-row').first()).toContainText('cloud');
    await pick(page, 'f-type', 'install-token.');
    await expect(page.getByTestId('activity-row')).toHaveCount(2);
    await page.reload();
    await expect(page.getByTestId('f-type')).toHaveAttribute('data-value', 'install-token.');
    await page.getByTestId('activity-row').first().click();
    await expect(page.getByTestId('activity-drawer')).toContainText('install-token.created');
    await expect(page.getByTestId('activity-data')).toContainText('siteSlug');
  });

  test('activities upload shows rejected rows of a tampered bundle', async ({ page }) => {
    await login(page, 'admin', `${D}/activities/upload`);
    await page.getByTestId('file-input').setInputFiles({ name: 'app.opmlog', mimeType: 'application/octet-stream', buffer: Buffer.from('opmlog/1 TAMPER') });
    await page.getByTestId('upload-submit').click();
    await expect(page.getByTestId('upload-result')).toContainText('signature ok');
    await expect(page.getByTestId('r-rejected')).toHaveText('3');
    await expect(page.getByTestId('upload-errors')).toContainText('app:10');
    await page.getByTestId('upload-another').click();
    await expect(page.getByTestId('drop-zone')).toBeVisible();
  });

  test('send a file to a site, then delete it', async ({ page }) => {
    await login(page, 'admin', `${D}/sites/app/transfers`);
    await expect(page.getByTestId('transfer-row')).toHaveCount(3); // §21 #100 mock: saved, waiting for the site, on the site
    await page.getByTestId('send-file').click();
    await page.getByTestId('send-file-input').setInputFiles({ name: 'hello.txt', mimeType: 'text/plain', buffer: Buffer.from('hello site\n'.repeat(100)) });
    await page.getByTestId('send-note').fill('test note');
    await page.getByTestId('send-submit').click();
    await expectToast(page, 'success', 'hello.txt sent');
    const row = page.getByTestId('transfer-row').filter({ hasText: 'hello.txt' });
    await expect(row).toContainText('stored · waiting for the site');
    await expect(row).toContainText('test note');
    await row.getByTestId('delete-transfer').click();
    await page.getByTestId('confirm-ok').click();
    await expect(row).toContainText('expired');
  });

  test('receive: download a file sent by the site', async ({ page }) => {
    await login(page, 'admin', `${D}/sites/db/transfers`);
    const row = page.getByTestId('receive-row').first();
    await expect(row).toContainText('redis-dump');
    const dl = page.waitForEvent('download');
    await row.getByTestId('download').click();
    expect((await dl).suggestedFilename()).toBe('redis-dump-2026-10-05.rdb');
    await expect(row).toContainText('downloaded');
  });

  test('§21 #105 review the browser changes → commit (message, suggest) → discard', async ({ page }) => {
    await login(page, 'admin', `${D}/variables`);
    await page.getByTestId('val-greeting').fill('Hi there');
    await page.getByTestId('review-changes').click();
    await expect(page).toHaveURL(/compare\?review=working/);
    await expect(page.getByTestId('review-view')).toContainText('greeting');
    await page.getByTestId('review-commit').click();
    const dlg = page.getByTestId('commit-dialog');
    await expect(dlg.getByTestId('commit-submit')).toBeDisabled();
    await dlg.getByTestId('commit-suggest').click();
    await expect(dlg.getByTestId('commit-message')).toHaveValue(/greeting/);
    await dlg.getByTestId('commit-submit').click();
    await expectToast(page, 'success', 'Commit 3 recorded');
    await expect(page.getByTestId('nav-head')).toHaveText('commit 3');
    await expect(page.getByTestId('change-bar-count')).toHaveCount(0);
    // a new edit → not committed → discard
    await page.getByTestId('nav-variables').click();
    await page.getByTestId('val-greeting').fill('Hi again');
    await expect(page.getByTestId('nav-uncommitted')).toHaveText('1 not committed');
    await page.getByTestId('change-bar-discard').click();
    await page.getByTestId('pending-discard-dialog').getByTestId('confirm-ok').click();
    await expectToast(page, 'success', 'Changes discarded');
    await expect(page.getByTestId('nav-uncommitted')).toHaveCount(0);
    await expect(page.getByTestId('val-greeting')).toHaveValue('Hi there');
  });

  test('history: open a commit (what it changed)', async ({ page }) => {
    await login(page, 'admin', `${D}/history`);
    await page.getByTestId('history-open-1').click();
    await expect(page.getByTestId('history-row-1')).toContainText('release 1');
    await expect(page.getByTestId('history-detail')).toBeVisible();
  });

  test('viewer: read only, no publish / setup / write actions', async ({ page }) => {
    await login(page, 'viewer', `${D}/variables`);
    await expect(page.getByTestId('change-bar')).toHaveCount(0);
    await expect(page.getByTestId('change-bar-commit')).toHaveCount(0);
    await expect(page.getByTestId('nav-setup')).toHaveCount(0);
    await page.goto(D);
    await expect(page.getByTestId('card-head')).toBeVisible();
    await expect(page.getByTestId('edit-deployment')).toHaveCount(0);
  });
});
