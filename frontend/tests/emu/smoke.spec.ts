/**
 * Smoke test against the Firebase emulators + the real backend function (SPEC-final §20.1 frontend):
 * login, registry, service + register image, not-found page, template with a site + container, deployment
 * (access key), setup install command, variables draft save, diff + publish blockers.
 * Needs: backend emulators running (auth 9099, functions 5001) and an admin bootstrapped with
 *   OPM_E2E_EMAIL / OPM_E2E_PASSWORD (default admin@example.com / admin123).
 */
import { test, expect, type Page } from '@playwright/test';

const EMAIL = process.env.OPM_E2E_EMAIL ?? 'admin@example.com';
const PASSWORD = process.env.OPM_E2E_PASSWORD ?? 'admin123';
const RUN = Date.now().toString(36).slice(-5);

test.describe.configure({ mode: 'serial', timeout: 120_000 });

async function login(page: Page, next = '/app/deployments') {
  await page.goto(`/login?next=${encodeURIComponent(next)}`);
  await page.getByTestId('login-email').fill(EMAIL);
  await page.getByTestId('login-password').fill(PASSWORD);
  await page.getByTestId('login-submit').click();
  await page.waitForURL((u) => u.pathname.startsWith(next.split('?')[0]!), { timeout: 30_000 });
}
const ok = async (page: Page, text?: string | RegExp) => {
  const t = page.getByTestId('toast-success').last();
  await expect(t).toBeVisible({ timeout: 20_000 });
  if (text) await expect(t).toContainText(text);
};

let serviceUrl = '';
let templateUrl = '';
let deploymentUrl = '';

test('wrong password shows the login error', async ({ page }) => {
  await page.goto('/login');
  await page.getByTestId('login-email').fill(EMAIL);
  await page.getByTestId('login-password').fill('definitely-wrong');
  await page.getByTestId('login-submit').click();
  await expect(page.getByTestId('login-error')).toContainText('Email or password is wrong');
});

test('registry: create + test login', async ({ page }) => {
  await login(page, '/app/registries');
  await page.getByTestId('new-registry').click();
  await page.getByTestId('reg-label').fill(`E2E registry ${RUN}`);
  await page.getByTestId('reg-username').fill('e2e-bot');
  await page.getByTestId('reg-password').fill('not-a-real-token');
  await page.getByTestId('reg-save').click();
  await ok(page, 'Registry created');
  const row = page.getByTestId('registry-row').filter({ hasText: `E2E registry ${RUN}` });
  await expect(row).toBeVisible();
  await row.getByTestId('test-registry').click();
  // Docker Hub answers (ok or failed) — either way the row gets a last check
  await expect(row.locator('.badge').first()).toBeVisible({ timeout: 30_000 });
});

test('service + register image', async ({ page }) => {
  await login(page, '/app/services');
  await page.getByTestId('new-service').click();
  await page.getByTestId('svc-label').fill(`E2E cache ${RUN}`);
  await page.getByTestId('svc-slug').fill(`e2e-cache-${RUN}`);
  await page.getByTestId('svc-icon').fill('database');
  await page.getByTestId('svc-repo').fill('library/redis');
  await page.getByTestId('svc-save').click();
  await expect(page.getByTestId('page-title')).toHaveText(`E2E cache ${RUN}`, { timeout: 20_000 });
  serviceUrl = new URL(page.url()).pathname;
  await page.getByTestId('register-image').click();
  await page.getByTestId('ri-tag').fill('7.4-alpine');
  await expect(page.getByTestId('ri-registry')).toContainText(`E2E registry ${RUN}`);
  await page.getByTestId('ri-registry').click();
  await page.getByTestId('ri-registry-list').getByRole('option', { name: new RegExp(`E2E registry ${RUN}`) }).first().click();
  await page.getByTestId('ri-add-port').click();
  await page.getByTestId('ri-port-row').first().getByLabel('Port name').fill('tcp');
  await page.getByTestId('ri-port-row').first().getByLabel('Port number').fill('6379');
  await page.getByTestId('ri-submit').click();
  await expect(page.getByTestId('page-title')).toContainText('7.4-alpine', { timeout: 20_000 });
  await expect(page.getByTestId('status-pending').or(page.getByTestId('status-copying')).or(page.getByTestId('status-failed')).or(page.getByTestId('status-ready')).first()).toBeVisible();
});

test('template with a site and a container', async ({ page }) => {
  await login(page, '/app/templates');
  await page.getByTestId('new-template').click();
  await page.getByTestId('nt-label').fill(`E2E template ${RUN}`);
  await page.getByTestId('nt-create').click();
  await expect(page.getByTestId('template-version')).toHaveText('v1', { timeout: 20_000 });
  templateUrl = new URL(page.url()).pathname;
  await page.goto(`${templateUrl}/variables`);
  await page.getByTestId('add-variable').click();
  await page.getByTestId('var-slug').fill('cache-port');
  await page.getByTestId('var-label').fill('Cache port');
  await page.getByTestId('var-type').selectOption('number');
  await page.getByTestId('var-value').fill('16380');
  await page.getByTestId('var-save').click();
  await ok(page, 'cache-port added');
  await page.goto(`${templateUrl}/sites`);
  await page.getByTestId('add-site').click();
  await page.getByTestId('site-slug').fill('cache');
  await page.getByTestId('site-label').fill('Cache site');
  // host: auto-detect (default)
  await page.getByTestId('site-save').click();
  await ok(page, 'Site cache added');
  await page.waitForURL(`**${templateUrl}/sites/cache/containers`);
  await page.getByTestId('add-container').click();
  await page.waitForURL(`**${templateUrl}/sites/cache/containers/new`);
  await page.getByTestId('c-service').click();
  await page.getByTestId('c-service').fill(`e2e-cache-${RUN}`);
  await page.getByTestId('c-service-list').locator(`[data-testid="combo-option"][data-value="e2e-cache-${RUN}"]`).click();
  await expect(page.getByTestId('container-editor')).toBeVisible();
  await page.getByTestId('port-tcp-published').click();
  await page.getByTestId('hostport-tcp-expr').click();
  await page.getByTestId('hostport-tcp-input').fill('{{ var.cache-port }}');
  await expect(page.getByTestId('hostport-tcp-preview')).toContainText('16380');
  await page.getByTestId('save-container').click();
  await ok(page, 'Site cache saved');
  await expect(page.getByTestId('template-version')).toHaveText('v4');
});

test('deployment from the template: access key, setup, draft, diff', async ({ page }) => {
  await login(page, '/app/deployments');
  await page.getByTestId('new-deployment').click();
  await page.getByTestId('dep-label').fill(`E2E deployment ${RUN}`);
  const opts = await page.getByTestId('dep-template').locator('option').allTextContents();
  const hit = opts.find((o) => o.includes(`E2E template ${RUN}`));
  expect(hit).toBeTruthy();
  await page.getByTestId('dep-template').selectOption({ label: hit! });
  await page.getByTestId('dep-save').click();
  await page.waitForURL('**/setup', { timeout: 20_000 });
  await expect(page.getByTestId('access-key-value')).toHaveText(/^([A-Z2-7]{4}-){5}[A-Z2-7]{4}$/, { timeout: 20_000 });
  deploymentUrl = new URL(page.url()).pathname.replace(/\/setup$/, '');
  const site = page.getByTestId('setup-site-cache');
  await site.getByTestId('create-install').click();
  await expect(site.getByTestId('install-cmd-value')).toContainText('Authorization: Bearer opi_');
  // variables: change a value in the draft
  await page.goto(`${deploymentUrl}/variables`);
  await page.getByTestId('val-cache-port').fill('16390');
  await page.getByTestId('save-draft').click();
  await ok(page, 'Saved 1 value');
  // diff: no head yet; the image import is not ready on the emulator → blockers or publishable
  await page.goto(`${deploymentUrl}/diff`);
  await expect(page.locator('body')).toContainText('No head yet');
  await expect(page.getByTestId('diff-row').first()).toBeVisible();
});

test('cleanup is not needed: data is unique per run', async () => {
  expect(serviceUrl && templateUrl && deploymentUrl).toBeTruthy();
});
