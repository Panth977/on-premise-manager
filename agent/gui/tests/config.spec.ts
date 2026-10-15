import { test, expect } from './fixtures';
import { login, open, shot, pickOption } from './helpers';

test('secrets: masked, View reveals for 60 s (logged), new value + generate → local changes', async ({ page }) => {
  await login(page, '/_opm/app/secrets');
  await expect(page.getByTestId('secret-api-token-value')).toHaveText('•••');
  await page.getByTestId('secret-api-token-view').click();
  await expect(page.getByTestId('secret-api-token-value')).toHaveText('Zx8Kq2Lm9Np4Rs7Tv1Wy3Ab5');
  await expect(page.getByText('visible for 60 s')).toBeVisible();
  await shot(page, 'secrets-revealed');
  await page.getByTestId('secret-api-token-hide').click();
  await expect(page.getByTestId('secret-api-token-value')).toHaveText('•••');
  await page.getByTestId('new-api-token').fill('a-new-token-value');
  await expect(page.getByTestId('local-edit-bar')).toContainText('1 change (not committed)');
  await page.getByRole('button', { name: 'Generate' }).click();
  await expect(page.getByText('new value generated')).toBeVisible();
  await shot(page, 'secrets-edited');
  await page.getByTestId('review-changes').click();
  await expect(page.getByTestId('review-drawer').getByTestId('diff')).toContainText('api-token');
  await expect(page.getByTestId('review-drawer').getByTestId('diff')).not.toContainText('a-new-token-value');
});

test('files: edit text content in the wide drawer → uploaded blob → changed', async ({ page }) => {
  await login(page, '/_opm/app/files');
  await shot(page, 'files');
  await page.getByTestId('edit-banner').click();
  const ed = page.getByTestId('file-drawer').getByTestId('code-editor');
  await expect(ed).toHaveValue('Welcome to the hello-world deployment\n');
  await ed.fill('Welcome to the plant, night shift\n');
  await shot(page, 'files-editor');
  await page.getByTestId('file-done').click();
  await expect(page.getByTestId('files')).toContainText('changed');
  await expect(page.getByTestId('local-edit-bar')).toContainText('1 change (not committed)');
  // edits add up across pages: change a variable too
  await page.getByRole('link', { name: 'Variables' }).click();
  await pickOption(page, 'var-theme', 'blue');
  await expect(page.getByTestId('local-edit-bar')).toContainText('2 changes (not committed)');
});

test('config: containers section, container editor (validation), edge editor + Test a URL, settings section', async ({ page }) => {
  await login(page, '/_opm/app/app/config');
  await expect(page.getByTestId('containers')).toContainText('hello-api');
  await shot(page, 'config-containers');
  await page.getByTestId('containers').getByRole('link', { name: 'hello-api', exact: true }).click();
  await page.waitForURL(/config\/containers\/hello-api$/);
  // site db has host auto (null): site.db.host = its advertised address
  await expect(page.getByText('10.0.1.11:16379').first()).toBeVisible();
  // shared container form (§21 #102): Memory = limit toggle + stepper
  if (!(await page.getByTestId('container-memory').count())) await page.getByTestId('memory-limit').click();
  await page.getByTestId('container-memory').fill('16');
  await expect(page.getByTestId('local-edit-bar')).toContainText('1 error');
  await expect(page.getByTestId('change-bar-commit')).toBeDisabled();
  await page.getByTestId('container-memory').fill('256');
  await expect(page.getByTestId('change-bar-commit')).toBeEnabled();
  await page.getByTestId('add-env').click();
  await page.getByRole('combobox', { name: 'Env name' }).last().fill('EXTRA_NOTE');
  await page.getByRole('combobox', { name: 'Env name' }).last().press('Escape');
  const extraVal = page.getByTestId('env-row-EXTRA_NOTE').locator('input[aria-label="Value of EXTRA_NOTE"]');
  await extraVal.fill('{{ var.nope }}');
  await expect(page.getByText('E_UNKNOWN_PATH').first()).toBeVisible();
  await shot(page, 'container-editor');
  await extraVal.fill('{{ var.theme | upper }}');
  await expect(page.getByText('GREEN').first()).toBeVisible();

  await page.getByTestId('nav-edge').click();
  await page.waitForURL(/config\/edge$/);
  await page.getByTestId('add-proxy').click();
  const rd = page.getByTestId('route-editor');
  await rd.getByTestId('route-path').fill('/metrics');
  await rd.getByTestId('route-id').fill('metrics');
  await rd.getByTestId('route-path-mode-strip').click();
  await shot(page, 'edge-route-drawer');
  await page.getByTestId('route-apply').click();
  await page.waitForURL(/config\/edge$/);
  await expect(page.getByTestId('routes')).toContainText('metrics');
  await page.getByTestId('test-url-input').fill('http://hello.acme.internal:8081/api/count?x=1');
  await page.getByTestId('test-run').click();
  await expect(page.getByTestId('test-result')).toContainText('proxy');
  await expect(page.getByTestId('test-result')).toContainText('http://hello-api:8080/count?x=1');
  await page.getByTestId('test-url-input').fill('http://hello.acme.internal:8081/old/thing');
  await page.getByTestId('test-run').click();
  await expect(page.getByTestId('test-result')).toContainText('308');
  await shot(page, 'edge-editor');
  await page.getByTestId('add-host').click();
  await page.getByTestId('host-name-input').fill('*.acme.internal');
  await page.getByTestId('host-tls-acme').click();
  await page.getByTestId('host-apply').click();
  await expect(page.getByTestId('host-apply')).toBeDisabled();
  await shot(page, 'edge-host-drawer');
  await page.getByTestId('host-tls-internal').click();
  await page.getByTestId('host-apply').click();

  await page.getByTestId('nav-settings').click();
  await expect(page.getByTestId('site-host-preview')).toContainText('127.0.0.1');
  await shot(page, 'config-site');
  await page.getByTestId('review-changes').click();
  await expect(page.getByTestId('review-drawer').getByTestId('diff')).toBeVisible();
  await shot(page, 'config-review');
});

test('all-sites config summary + sites config cards', async ({ page }) => {
  await login(page, '/_opm/app/*/config');
  await expect(page.getByTestId('config-summary')).toContainText('db');
  await open(page, '/_opm/app/sites');
  await expect(page.getByTestId('site-cards')).toContainText('redis');
  await shot(page, 'sites-config');
});
