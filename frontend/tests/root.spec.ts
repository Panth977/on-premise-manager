import { test, expect } from '@playwright/test';
import { login, expectToast, SEED, pickIcon, pickCombo, pick } from './helpers';

test.describe('registries', () => {
  test('create (preset + anonymous toggle), test login with a repository, reveal, delete conflict', async ({ page }) => {
    await login(page, 'admin', '/app/registries');
    await expect(page.getByTestId('registry-row')).toHaveCount(4);
    await expect(page.getByTestId('registry-row').filter({ hasText: 'Docker Hub' }).first()).toContainText('anonymous');
    await page.getByTestId('new-registry').click();
    await page.getByTestId('reg-save').click();
    await expect(page.getByTestId('registry-modal').getByTestId('field-error').first()).toBeVisible();
    await page.getByTestId('reg-label').fill('GHCR');
    await page.getByTestId('reg-provider-ghcr').click();
    await expect(page.getByTestId('reg-url')).toHaveCount(0); // presets: no free URL
    await page.getByTestId('reg-anonymous').click();
    await expect(page.getByTestId('reg-username')).toHaveCount(0);
    await page.getByTestId('reg-anonymous').click();
    await page.getByTestId('reg-username').fill('bot');
    await page.getByTestId('reg-password').fill('ghp_secret');
    await page.getByTestId('reg-save').click();
    await expectToast(page, 'success', 'Registry created');
    const row = page.getByTestId('registry-row').filter({ hasText: 'GHCR' });
    await expect(row).toContainText('https://ghcr.io');
    await row.getByTestId('test-registry').click();
    await page.getByTestId('test-run').click();
    await expectToast(page, 'success', 'login ok');
    await page.getByTestId('registry-test-modal').getByRole('button', { name: 'Close' }).last().click();
    await expect(row).toContainText('ok');
    await row.locator('[data-testid$="-view"]').click();
    await expect(row.locator('[data-testid$="-value"]')).toHaveText('ghp_secret');
    // test login of Docker Hub (acme-bot) with a repository picked from the list
    const bot = page.getByTestId('registry-row').filter({ hasText: 'acme-bot' });
    await bot.getByTestId('test-registry').click();
    await pickCombo(page, 'test-repo', 'library/redis', 'redis');
    await page.getByTestId('test-run').click();
    await expect(page.getByTestId('test-result')).toContainText('library/redis');
    await page.getByTestId('test-repo').fill('acme/nope');
    await page.getByTestId('test-run').click();
    await expect(page.getByTestId('test-result')).toContainText('not found');
    await page.getByTestId('registry-test-modal').getByRole('button', { name: 'Close' }).last().click();
    await expect(page.getByTestId('registry-test-modal')).toHaveCount(0);
    // Artifact Registry is used by images → conflict
    const ar = page.getByTestId('registry-row').filter({ hasText: 'Artifact Registry' });
    await ar.getByRole('button', { name: /Delete Artifact/ }).click();
    await page.getByTestId('confirm-ok').click();
    await expectToast(page, 'error', 'Used by images');
    await expect(page.getByTestId('confirm-dialog')).toContainText('hello-web:v1');
  });
});

test.describe('services + images', () => {
  test('create service: name (id) from the label, icon picker, no repository', async ({ page }) => {
    await login(page, 'admin', '/app/services');
    await expect(page.getByTestId('service-row')).toHaveCount(10); // round E mock: + northwind-admin, pg-admin, nodered, northwind-web; §21 #83: + node-red; §21 #99: + postgresql
    await page.getByTestId('new-service').click();
    await expect(page.getByTestId('service-modal')).not.toContainText('Repository');
    await page.getByTestId('svc-label').fill('Redis');
    await expect(page.getByTestId('svc-slug')).toHaveValue('redis');
    await page.getByTestId('svc-save').click();
    await expectToast(page, 'error');
    await expect(page.getByTestId('service-modal')).toContainText('already used');
    await page.getByTestId('svc-label').fill('Postgres');
    await expect(page.getByTestId('svc-slug')).toHaveValue('postgres');
    await pickIcon(page, 'svc-icon', 'database');
    await expect(page.getByTestId('svc-icon')).toContainText('database');
    await page.getByTestId('svc-save').click();
    await page.waitForURL(/\/app\/services\/postgres$/);
    await expect(page.getByTestId('page-title')).toHaveText('Postgres');
    await expect(page.getByTestId('empty-state')).toContainText('No images yet');
  });

  test('register image: pasted reference, read from registry, register, import becomes ready', async ({ page }) => {
    test.setTimeout(60_000);
    await login(page, 'admin', `/app/services/${SEED.svcApi}/register-image`);
    await expect(page.getByTestId('ri-version')).toHaveValue('v2');
    await page.getByTestId('ri-ref').fill('registry.example.com/opm-test/hello-api:1.2.0');
    await page.keyboard.press('Escape');
    await expect(page.getByTestId('ri-ref-parts')).toContainText('example/opm-test/hello-api');
    await expect(page.getByTestId('ri-registry')).toHaveAttribute('data-value', SEED.registryAr);
    await page.getByTestId('ri-read').click();
    await expectToast(page, 'success', 'Filled from the registry');
    await expect(page.getByTestId('ri-port-row')).toHaveCount(1);
    await expect(page.getByTestId('ri-env-row')).toHaveCount(2);
    await page.getByTestId('hc-type-http').click();
    await page.getByTestId('ri-submit').click();
    await page.waitForURL(/\/image\/v2$/);
    await expect(page.getByTestId('page-title')).toContainText('hello-api:v2');
    await expect(page.getByTestId('image-reference')).toHaveText('registry.example.com/opm-test/hello-api:1.2.0');
    await expect(page.locator('header').getByTestId('status-ready')).toBeVisible({ timeout: 25_000 });
  });

  test('register image client validation', async ({ page }) => {
    await login(page, 'admin', `/app/services/${SEED.svcApi}/register-image`);
    await page.getByTestId('ri-add-port').click();
    await page.getByTestId('ri-submit').click();
    await expect(page.getByTestId('form-summary')).toBeVisible();
    await expect(page.getByTestId('ri-port-row').getByTestId('field-error').first()).toBeVisible();
  });

  test('image metadata save and reset', async ({ page }) => {
    await login(page, 'admin', `/app/services/${SEED.svcWeb}/image/v1`);
    await expect(page.getByTestId('im-save')).toBeDisabled();
    await page.getByTestId('ri-desc').fill('Web page of the hello app');
    await expect(page.getByTestId('im-save')).toBeEnabled();
    await page.getByTestId('im-save').click();
    await expectToast(page, 'success', 'Image saved');
    await expect(page.getByTestId('im-save')).toBeDisabled();
    await page.reload();
    await expect(page.getByTestId('ri-desc')).toHaveValue('Web page of the hello app');
    // used image cannot be deleted
    await page.getByTestId('delete-image').click();
    await page.getByTestId('confirm-ok').click();
    await expect(page.getByTestId('delete-usage')).toContainText('ACME plant');
  });

  test('failed import: edit source, then retry', async ({ page }) => {
    await login(page, 'admin', `/app/services/${SEED.svcWeb}/image/v2`);
    await expect(page.getByTestId('import-error')).toBeVisible();
    await page.getByTestId('edit-source').click();
    await expect(page.getByTestId('src-ref')).toHaveValue(/hello-web:1\.1\.0-rc1$/);
    await page.getByTestId('src-ref').fill('registry.example.com/opm-test/hello-web:1.2.0');
    await page.keyboard.press('Escape');
    await page.getByTestId('src-save-retry').click();
    await expectToast(page, 'success', 'import restarted');
    await expect(page.getByTestId('image-reference')).toContainText('hello-web:1.2.0');
    await expect(page.locator('header').getByTestId('status-ready')).toBeVisible({ timeout: 25_000 });
  });

  test('old image links (?tag=) resolve', async ({ page }) => {
    await login(page, 'admin', `/app/services/${SEED.svcWeb}/image?tag=v1`);
    await page.waitForURL(/\/image\/v1$/);
    await expect(page.getByTestId('page-title')).toContainText('hello-web:v1');
  });
});

test.describe('not-found pages', () => {
  test('create, preview, copy', async ({ page }) => {
    await login(page, 'admin', '/app/not-found-pages');
    await page.getByTestId('new-page').click();
    await page.getByTestId('nfp-label').fill('Branded 404');
    await expect(page.getByTestId('nfp-bytes')).toContainText('bytes');
    await page.getByTestId('nfp-save').click();
    await page.waitForURL(/\/app\/not-found-pages\/[A-Za-z0-9]{20}$/);
    await expect(page.getByTestId('page-title')).toHaveText('Branded 404');
    await expect(page.getByTestId('read-only')).toBeVisible();
    const frame = page.frameLocator('[data-testid=page-preview]');
    await expect(frame.locator('h1')).toHaveText('404');
    await page.getByTestId('copy-page').click();
    await expect(page.getByTestId('nfp-label')).toHaveValue('Copy of Branded 404');
    await page.getByTestId('nfp-save').click();
    await expect(page.getByTestId('page-title')).toHaveText('Copy of Branded 404');
    await expect(page.getByTestId('delete-page')).toBeEnabled();
  });
  test('used page cannot be deleted', async ({ page }) => {
    await login(page, 'admin', `/app/not-found-pages/${SEED.page404}`);
    await expect(page.getByTestId('delete-page')).toBeDisabled();
  });
});

test.describe('tags', () => {
  test('create, edit, delete', async ({ page }) => {
    await login(page, 'admin', '/app/tags');
    await page.getByTestId('new-tag').click();
    await page.getByTestId('tag-label').fill('Lab');
    await expect(page.getByTestId('tag-slug')).toHaveValue('lab');
    await pickIcon(page, 'tag-icon', 'flask-conical');
    await page.getByTestId('swatch-16a34a').click();
    await expect(page.getByTestId('tag-color-hex')).toHaveValue('#16a34a');
    await page.getByTestId('tag-save').click();
    await expectToast(page, 'success', 'Tag lab created');
    const row = page.getByTestId('tag-row').filter({ hasText: 'lab' }).first();
    await row.getByRole('button', { name: 'Edit lab' }).click();
    await page.getByTestId('tag-label').fill('Laboratory');
    await page.getByTestId('tag-save').click();
    await expectToast(page, 'success', 'Tag saved');
    await expect(page.getByTestId('tags-table')).toContainText('Laboratory');
    // delete pilot (used by ACME + ops access rules)
    await page.getByTestId('tag-row').filter({ hasText: 'pilot' }).getByRole('button', { name: 'Delete pilot' }).click();
    await expect(page.getByTestId('confirm-dialog')).toContainText('1 deployment');
    await page.getByTestId('confirm-ok').click();
    await expectToast(page, 'success', /1 deployment\(s\) and 3 access rule/);
  });
});

test.describe('accounts', () => {
  test('create account, link modal, access rules, full admin', async ({ page }) => {
    await login(page, 'admin', '/app/accounts');
    await page.getByTestId('new-account').click();
    await page.getByTestId('acc-name').fill('Nia New');
    await page.getByTestId('acc-email').fill('nia@example.com');
    await page.getByTestId('acc-save').click();
    await expect(page.getByTestId('link-modal')).toBeVisible();
    await expect(page.getByTestId('created-password-value')).toHaveText(/^[A-Za-z0-9]{20}$/);
    await expect(page.getByTestId('created-password-countdown')).toBeVisible();
    await page.getByTestId('link-continue').click();
    await page.waitForURL(/\/app\/accounts\/[A-Za-z0-9]{20}$/);
    await expect(page.getByTestId('page-title')).toHaveText('Nia New');
    await page.getByTestId('add-rule').click();
    const rule = page.getByTestId('rule-row').first();
    await pick(page, 'rule-resource', 'deployment.config', rule);
    await rule.getByTestId('rule-action-write').check();
    await rule.getByTestId('rule-scope-tags').click();
    await page.getByTestId('save-access').click();
    await expect(rule.getByTestId('field-error').first()).toBeVisible(); // tags empty
    await rule.getByTestId('rule-tags').locator('[role=combobox]').click();
    await page.getByRole('option', { name: /Pilot/ }).click();
    await page.keyboard.press('Escape');
    await page.getByTestId('save-access').click();
    await expectToast(page, 'success', 'Access saved');
    await page.getByTestId('make-admin').click();
    await page.getByTestId('confirm-ok').click();
    await expect(page.getByTestId('rule-row')).toHaveCount(13);
    await page.getByTestId('save-access').click();
    await expectToast(page, 'success', 'Access saved');
    await expect(page.locator('header.ph')).toContainText('admin');
    await page.getByTestId('reset-link-regenerate').click();
    await page.getByTestId('reset-link-confirm').getByTestId('confirm-ok').click();
    await expect(page.getByTestId('reset-link-value')).toContainText('resetPassword');
  });
  test('cannot remove the last admin access from yourself', async ({ page }) => {
    await login(page, 'admin', `/app/accounts/${SEED.uidAdmin}`);
    await page.getByTestId('rule-row').first().getByRole('button', { name: /Remove rule/ }).click();
    const accRule = page.getByTestId('rule-row').filter({ has: page.locator('[data-testid="rule-resource"][data-value="accounts"]') });
    await accRule.getByRole('button', { name: /Remove rule/ }).click();
    await page.getByTestId('save-access').click();
    await expectToast(page, 'error', 'last account');
  });
});

test.describe('audit log', () => {
  test('load more', async ({ page }) => {
    await login(page, 'admin', '/app/audit-log?limit=4');
    await expect(page.getByTestId('audit-row')).toHaveCount(4);
    await page.getByTestId('load-more').click();
    await expect(page.getByTestId('audit-row')).toHaveCount(8);
    await page.getByTestId('load-more').click();
    await expect(page.getByTestId('audit-row')).toHaveCount(11);
    await expect(page.getByTestId('load-more')).toHaveCount(0);
  });
});

test.describe('deployments list', () => {
  test('filter by tag, search, create → access key → setup', async ({ page }) => {
    await login(page, 'admin', '/app/deployments');
    await expect(page.getByTestId('deployment-card')).toHaveCount(3); // + §21 #101 Node-RED plant (tag gov)
    await page.getByTestId('filter-pilot').click();
    await expect(page).toHaveURL(/tag=pilot/);
    await expect(page.getByTestId('deployment-card')).toHaveCount(1);
    await page.getByTestId('filter-all').click();
    await expect(page.getByTestId('deployment-card')).toHaveCount(3);
    await page.getByTestId('search').fill('north');
    await expect(page.getByTestId('deployment-card')).toHaveCount(1);
    await page.getByTestId('search').fill('');
    await page.getByTestId('new-deployment').click();
    await page.getByTestId('dep-label').fill('South depot');
    await expect(page.getByTestId('dep-template')).toHaveValue('Hello world');
    await page.getByTestId('dep-save').click();
    // no modal, no checkbox: Setup shows the key in RevealValue with its 60 s ring
    await page.waitForURL(/\/app\/deployments\/[A-Za-z0-9]{20}\/setup$/);
    await expect(page.getByTestId('access-key-value')).toHaveText(/^([A-Z2-7]{4}-){5}[A-Z2-7]{4}$/);
    await expect(page.getByTestId('access-key-countdown')).toBeVisible();
    await expect(page.getByTestId('access-key-modal')).toHaveCount(0);
    await expect(page.getByTestId('saved-key')).toHaveCount(0);
  });
  test('ops (tag-scoped) must pick a pilot tag', async ({ page }) => {
    await login(page, 'ops', '/app/deployments');
    await expect(page.getByTestId('deployment-card')).toHaveCount(2);
    await page.getByTestId('new-deployment').click();
    await page.getByTestId('dep-label').fill('Pilot site');
    await page.getByTestId('dep-src-empty').click();
    await page.getByTestId('dep-save').click();
    await expect(page.getByTestId('deployment-drawer')).toContainText('Your access needs one of: pilot');
  });
});

test.describe('read-only account', () => {
  test('viewer sees no write buttons', async ({ page }) => {
    await login(page, 'viewer', '/app/deployments');
    await expect(page.getByTestId('deployment-card').first()).toBeVisible();
    await expect(page.getByTestId('new-deployment')).toHaveCount(0);
    await page.goto('/app/registries');
    await expect(page.getByTestId('registry-row').first()).toBeVisible();
    await expect(page.getByTestId('new-registry')).toHaveCount(0);
    await expect(page.getByTestId('test-registry')).toHaveCount(0);
    await expect(page.getByTestId('read-only')).toBeVisible();
    await page.goto('/app/services');
    await expect(page.getByTestId('service-row').first()).toBeVisible();
    await expect(page.getByTestId('new-service')).toHaveCount(0);
    await page.goto(`/app/services/${SEED.svcWeb}`);
    await expect(page.getByTestId('register-image')).toHaveCount(0);
    await expect(page.getByTestId('edit-service')).toHaveCount(0);
    await page.goto('/app/tags');
    await expect(page.getByTestId('tag-row').first()).toBeVisible();
    await expect(page.getByTestId('new-tag')).toHaveCount(0);
    await page.goto('/app/accounts');
    await expect(page.getByTestId('account-row').first()).toBeVisible();
    await expect(page.getByTestId('new-account')).toHaveCount(0);
    await page.goto('/app/audit-log');
    await expect(page.getByTestId('empty-state')).toContainText('Admins only');
  });
});
