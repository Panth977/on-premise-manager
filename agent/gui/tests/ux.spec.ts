import { test, expect } from './fixtures';
import { expectOnTop, login, shot, pickOption } from './helpers';

const EDITOR = '/_opm/app/app/config/containers/hello-api';

test('env choice: options dropdown (default marked), expression toggle, V_OPTION inline; legacy list env', async ({ page }) => {
  await login(page, EDITOR);
  // pick the env name from the image env picker
  await page.getByTestId('add-env').click();
  const name = page.getByRole('combobox', { name: 'Env name' }).last();
  await name.click();
  await name.fill('NODE');
  await expect(page.getByRole('listbox', { name: 'Env name options' })).toBeVisible();
  await page.getByRole('option', { name: /NODE_ENV/ }).click();
  const choice = page.getByTestId('env-choice-NODE_ENV');
  await expect(choice).toHaveAttribute('data-value', 'worker');
  await choice.click();
  await expect(page.getByTestId('env-choice-NODE_ENV-opt-worker')).toContainText('default');
  await expect(page.getByTestId('env-choice-NODE_ENV-list').getByRole('option')).toHaveCount(6);
  await page.getByTestId('env-choice-NODE_ENV-opt-migrate').click();
  await expect(page.getByTestId('resolved-preview')).toContainText('/app/helper/config/env/migrate.ts');
  await expect(page.getByTestId('vol-env-config-path')).toContainText('/app/helper/config/env/migrate.ts');
  // legacy list env (value: string[]) becomes a choice too
  await page.getByTestId('add-env').click();
  await page.getByRole('combobox', { name: 'Env name' }).last().fill('LOG_LEVEL');
  await page.keyboard.press('Escape'); // an image env name moves the row to its image-env row (shared form)
  await expect(page.getByTestId('env-choice-LOG_LEVEL')).toHaveAttribute('data-value', 'info');
  await page.getByTestId('env-choice-LOG_LEVEL').click();
  const lv = page.getByTestId('env-choice-LOG_LEVEL-list').getByRole('option');
  await expect(lv).toHaveCount(3);
  await expect(lv.nth(0)).toHaveAttribute('data-value', 'info');
  await expect(lv.nth(0)).toContainText('default');
  await expect(lv.nth(1)).toHaveAttribute('data-value', 'debug');
  await expect(lv.nth(2)).toHaveAttribute('data-value', 'warn');
  await page.keyboard.press('Escape');
  // advanced: expression mode → a value outside the options is V_OPTION
  await page.getByTestId('env-expr-NODE_ENV').click();
  await page.getByTestId('env-value-NODE_ENV-input').fill('staging');
  // shared container form (§21 #102): an image env name moves to its image-env row
  const row = page.getByTestId('ienv-NODE_ENV');
  await expect(row.getByTestId('env-issue-NODE_ENV')).toContainText('V_OPTION');
  await expect(row.getByTestId('env-issue-NODE_ENV')).toContainText('integration, dc, worker, pg-listen, terminal, migrate');
  await expect(page.getByTestId('change-bar-commit')).toBeDisabled();
  await shot(page, 'ux-env-choice');
  // back to the list → a valid option again
  await page.getByTestId('env-expr-NODE_ENV').click();
  await expect(page.getByTestId('env-choice-NODE_ENV')).toHaveAttribute('data-value', 'worker');
  await expect(row.getByTestId('env-issue-NODE_ENV')).toHaveCount(0);
});

test('mount path: container.env autocomplete, resolved preview, V_PATH for relative paths', async ({ page }) => {
  await login(page, EDITOR);
  // image-volume row: the effective (image) path is shown; "Edit path" reveals the expression input (empty = image path)
  await expect(page.getByTestId('vol-data-path')).toContainText('/data');
  await page.getByTestId('vol-data-edit-path').click();
  const mp = page.getByTestId('vol-data-mount-input');
  await expect(mp).toHaveValue('');
  await expect(page.getByTestId('vol-data-mount')).toContainText('image: /data');
  await mp.pressSequentially('/srv/{{ container.env.N');
  const list = page.getByTestId('vol-data-mount-ac');
  await expect(list).toBeVisible();
  await expect(list).toContainText('container.env.NODE_ENV');
  await expect(list).not.toContainText('container.serviceId');
  await shot(page, 'ux-mount-autocomplete', false);
  await page.getByRole('option', { name: /container\.env\.NODE_ENV/ }).click();
  await expect(mp).toHaveValue('/srv/{{ container.env.NODE_ENV }}');
  const cell = page.getByTestId('vol-row-data');
  await expect(cell.getByTestId('vol-data-mount-preview')).toHaveText(/→\s*\/srv\/worker/);
  // the implicit image volume resolves through the image default too
  await expect(page.getByTestId('vol-env-config-path')).toContainText('/app/helper/config/env/worker.ts');
  // relative → V_PATH (save level) inline
  await mp.fill('srv/{{ container.env.NODE_ENV }}');
  await expect(cell.getByTestId('vol-data-relative')).toHaveText('must start with / — relative paths are invalid'); // V_PATH, said plainly
  // secrets are not allowed in a mount path
  await mp.fill('/srv/{{ secret.api-token }}');
  await expect(cell.getByTestId('vol-data-mount-preview')).toContainText('E_NOT_ALLOWED');
  await shot(page, 'ux-mount-path');
});

test('E_CYCLE between two env values is shown inline', async ({ page }) => {
  await login(page, EDITOR);
  for (const [n, other] of [
    ['CYC_A', 'CYC_B'],
    ['CYC_B', 'CYC_A'],
  ]) {
    await page.getByTestId('add-env').click();
    await page.getByRole('combobox', { name: 'Env name' }).last().fill(n);
    await page.getByRole('combobox', { name: 'Env name' }).last().press('Escape');
    await page.getByTestId(`env-row-${n}`).locator(`input[aria-label="Value of ${n}"]`).fill(`x-{{ container.env.${other} }}`);
  }
  await expect(page.getByTestId('env-row-CYC_A').locator('[data-testid$="-preview"]')).toContainText('E_CYCLE');
  await expect(page.getByTestId('env-row-CYC_B').locator('[data-testid$="-preview"]')).toContainText('E_CYCLE');
  await expect(page.getByTestId('env-row-CYC_A')).toContainText('container.env.CYC_A → container.env.CYC_B → container.env.CYC_A');
  await shot(page, 'ux-cycle');
  await page.getByTestId('env-row-CYC_B').locator('input[aria-label="Value of CYC_B"]').fill('plain');
  await expect(page.getByTestId('env-row-CYC_A').locator('[data-testid$="-preview"]')).toHaveText(/→\s*x-plain/);
});

test('catalog icons: lucide:server, null → default, data:image/png upload', async ({ page }) => {
  await login(page, '/_opm/app/app/config');
  const api = page.getByTestId('icon-hello-api').locator('[data-icon-kind]');
  await expect(api).toHaveAttribute('data-icon-kind', 'lucide');
  await expect(api).toHaveAttribute('data-icon', 'server');
  await expect(api.locator('svg')).toHaveCount(1);
  const web = page.getByTestId('icon-hello-web').locator('img');
  await expect(web).toHaveAttribute('data-icon-kind', 'image');
  await expect(web).toHaveAttribute('src', /^data:image\/png;base64,/);
  expect(await web.evaluate((i: HTMLImageElement) => i.complete && i.naturalWidth)).toBe(16);
  await page.goto('/_opm/app/db/config');
  await page.getByTestId('actor-email').fill('tester@acme.example');
  await page.getByTestId('actor-continue').click();
  const redis = page.getByTestId('icon-redis').locator('[data-icon-kind]');
  await expect(redis).toHaveAttribute('data-icon-kind', 'default');
  await expect(redis).toHaveAttribute('data-icon', 'box');
  await shot(page, 'ux-icons-db', false);
});

test('pickers: route service combobox + port dropdown, redirect status, timeout stepper, not-found picker, TLS segmented', async ({ page }) => {
  await login(page, '/_opm/app/app/config?tab=edge');
  await page.getByTestId('add-proxy').click();
  const rd = page.getByTestId('route-editor');
  await rd.getByTestId('route-path').fill('/web2/');
  await rd.getByTestId('route-id').fill('web2');
  const svc = rd.getByTestId('route-service');
  await svc.click();
  await expect(page.getByTestId('route-service-opt-hello-web')).toBeVisible();
  await expectOnTop(page, '.lbx:popover-open');
  await shot(page, 'ux-route-service-picker', false);
  await page.getByTestId('route-service-opt-hello-web').click();
  await expect(svc).toHaveAttribute('data-value', 'hello-web');
  await expect(rd.getByTestId('route-port')).toHaveAttribute('data-value', 'http');
  await rd.getByTestId('sec-timeouts-toggle').click();
  await rd.getByRole('button', { name: 'Increase Read timeout' }).click();
  await expect(rd.getByTestId('route-read-timeout')).toHaveValue('65');
  await rd.getByTestId('add-header').click();
  await rd.getByTestId('hdr-name-0').fill('X-Opm-Site');
  await rd.getByTestId('hdr-value-0-input').fill('{{ self.slug }}');
  await expect(rd.getByTestId('hdr-value-0-preview')).toContainText('app');
  await page.getByTestId('route-apply').click();
  await page.waitForURL(/config\/edge$/);
  await expect(page.getByTestId('routes')).toContainText('proxy → hello-web:http');
  await expect(page.getByTestId('routes')).toContainText('read 65s');

  await page.getByTestId('add-redirect').click();
  await page.waitForURL(/route\?new=redirect$/);
  await rd.getByTestId('route-path').fill('/legacy');
  await rd.getByTestId('route-id').fill('legacy');
  await rd.getByTestId('route-to-input').fill('/new');
  await page.getByTestId('route-status-302').click();
  await expect(page.getByTestId('route-status-302')).toHaveAttribute('aria-checked', 'true');
  await shot(page, 'ux-redirect-drawer', false);
  await page.getByTestId('route-apply').click();
  await page.waitForURL(/config\/edge$/);
  await expect(page.getByTestId('routes')).toContainText('redirect 302 → /new');

  await page.getByTestId('add-host').click();
  await page.getByTestId('host-tls-acme').click();
  await page.getByTestId('host-challenge-dns-01').click();
  const dns = page.getByTestId('host-dns');
  await dns.click();
  await page.getByTestId('host-dns-list').locator('[role="option"][data-value="cloudflare"]').click();
  await expect(dns).toHaveValue('Cloudflare');
  await shot(page, 'ux-host-drawer', false);
  await page.getByTestId('host-drawer').getByRole('button', { name: 'Cancel' }).click();

  await page.getByTestId('nav-settings').click();
  const nf = page.getByTestId('nf-picker');
  await expect(nf.locator('iframe')).toHaveCount(1);
  await expect(nf.getByRole('radio').nth(1)).toBeChecked();
  await nf.getByText('Built-in page').click();
  await expect(nf.getByRole('radio').first()).toBeChecked();
  await page.getByRole('button', { name: 'Increase agent GUI alias port' }).click();
  await expect(page.locator('#sh-alias')).toHaveValue('8091');
  await expect(page.getByTestId('local-edit-bar')).toContainText('(not committed)');
  await shot(page, 'ux-site-tab');
});

test('overlays are not clipped by the header: Change person popup + popover inside a drawer (1360)', async ({ page }) => {
  await login(page, '/_opm/app/app/config?tab=edge');
  await page.getByTestId('change-person').click();
  await expect(page.getByTestId('actor-popup')).toBeVisible();
  await expectOnTop(page, '[data-testid="actor-popup"] .inner');
  await shot(page, 'ux-change-person', false);
  await page.getByTestId('actor-email').fill('mira@acme.example');
  await page.getByTestId('actor-continue').click();
  await expect(page.getByTestId('actor-popup')).toBeHidden();
  await page.getByTestId('add-host').click();
  await expectOnTop(page, '[data-testid="host-drawer"] .inner');
  await page.getByTestId('host-tls-files').click();
  await page.getByTestId('host-key').click();
  await expectOnTop(page, '.lb:popover-open');
});

test('dark mode: container editor', async ({ page }) => {
  await page.emulateMedia({ colorScheme: 'dark' });
  await login(page, EDITOR);
  await page.getByTestId('vol-data-edit-path').click();
  await page.getByTestId('vol-data-mount-input').fill('/srv/{{ container.env.NODE_ENV }}');
  await shot(page, 'ux-editor-dark');
  await page.goto('/_opm/app/app/config?tab=site');
  await page.getByTestId('actor-email').fill('tester@acme.example');
  await page.getByTestId('actor-continue').click();
  await shot(page, 'ux-site-dark');
});
