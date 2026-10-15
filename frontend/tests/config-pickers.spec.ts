import { test, expect, type Page } from '@playwright/test';
import { login, SEED, pickCombo, pickSource } from './helpers';

/** Phase U part B: pickers in the template config editors (container / route), container.env expressions, resolved preview. */
const TPL = `/app/templates/${SEED.template}`;

/** open the full "new container" page of site app and pick core-api (choice env NODE_ENV, image volume config at /app/helper/config/env/{{ container.env.NODE_ENV }}.ts) */
async function addCore(page: Page) {
  await login(page, 'admin', `${TPL}/sites/app/containers`);
  await page.getByTestId('add-container').click();
  await page.waitForURL(`**${TPL}/sites/app/containers/new`);
  await pickCombo(page, 'c-service', SEED.svcCore, 'core');
  // version dropdown: newest first, newest ready preselected
  await expect(page.getByTestId('container-version')).toHaveAttribute('data-value', 'v2');
  await page.getByTestId('container-version').click();
  const opts = page.getByTestId('container-version-list').getByRole('option');
  await expect(opts.first()).toHaveAttribute('data-value', 'v2');
  await expect(opts.nth(1)).toHaveAttribute('data-value', 'v1');
  await expect(opts.first()).toContainText('docker.io/acme/core-api');
  await expect(opts.first()).toContainText('ready');
  await page.keyboard.press('Escape');
  // the name is suggested from the service slug
  await expect(page.getByTestId('c-name')).toHaveValue('core-api');
  await expect(page.getByTestId('container-editor')).toContainText('core-api:v2');
}

test.describe('config editor pickers (phase U)', () => {
  test('version dropdown, choice env + V_OPTION + expression toggle, container.env mount path', async ({ page }) => {
    await addCore(page);
    // version dropdown in the editor header
    await pickCombo(page, 'container-version', 'v1');
    await expect(page.getByTestId('container-version')).toHaveAttribute('data-value', 'v1');
    await pickCombo(page, 'container-version', 'v2');
    await expect(page.getByTestId('container-version')).toHaveAttribute('data-value', 'v2');

    // image volumes start "not mounted" (round D, warning V_VOLUME_NOT_MOUNTED); the required secret env is prefilled
    await expect(page.getByTestId('ienv-DB_PASSWORD')).toBeVisible();
    await expect(page.getByTestId('vol-config-source')).toContainText('Not mounted — pick a data volume');
    await expect(page.getByTestId('vol-row-config').locator('[data-code="V_VOLUME_NOT_MOUNTED"]')).toBeVisible();
    // + New data volume… declares <container>-<volume> on the site and selects it (read-write)
    await pickSource(page, 'vol-config-source', 'new-volume');
    await expect(page.getByTestId('vm-slug')).toHaveValue('core-api-config');
    await page.getByTestId('vm-save').click();
    await expect(page.getByTestId('vol-config-source')).toHaveAttribute('data-value', 'volume:core-api-config');
    await expect(page.getByTestId('vol-config-ro').locator('input')).not.toBeChecked();
    await expect(page.getByTestId('vol-config-path')).toHaveText('/app/helper/config/env/integration.ts');
    // data volume "Not mounted" (explicit, no warning) → left out of the mounts
    await pickSource(page, 'vol-data-source', 'none');
    await expect(page.getByTestId('vol-row-data').locator('[data-code="V_VOLUME_NOT_MOUNTED"]')).toHaveCount(0);
    await expect(page.getByTestId('sum-mounts')).not.toContainText('/data');

    // env override: the image env list shows the default; Override → dropdown of the options ("default" marked)
    await page.getByTestId('ienv-NODE_ENV-override').click();
    await expect(page.getByTestId('env-choice-NODE_ENV')).toHaveAttribute('data-value', 'integration');
    await page.getByTestId('env-choice-NODE_ENV').click();
    await expect(page.getByTestId('env-choice-NODE_ENV-list').locator('[data-value="integration"]')).toContainText('default');
    await page.getByTestId('env-choice-NODE_ENV-list').locator('[data-value="worker"]').click();
    await expect(page.getByTestId('env-choice-NODE_ENV')).toHaveAttribute('data-value', 'worker');
    await expect(page.getByTestId('pv-env-NODE_ENV')).toContainText('worker');
    await expect(page.getByTestId('vol-config-path')).toHaveText('/app/helper/config/env/worker.ts');

    // mount path override ("Edit path") with container.env autocomplete (no container.serviceId)
    await page.getByTestId('vol-config-edit-path').click();
    const mount = page.getByTestId('vol-config-mount-input');
    await mount.fill('/app/helper/config/env/{{ container.');
    await expect(page.getByRole('option', { name: /container\.env\.NODE_ENV/ })).toBeVisible();
    await expect(page.getByRole('option', { name: /container\.serviceId/ })).toHaveCount(0);
    await mount.fill('/app/helper/config/env/{{ container.env.NO');
    await page.getByRole('option', { name: /container\.env\.NODE_ENV/ }).click();
    await expect(mount).toHaveValue('/app/helper/config/env/{{ container.env.NODE_ENV }}');
    await mount.fill('/opt/env/{{ container.env.NODE_ENV }}.ts');
    await expect(page.getByTestId('vol-config-mount-preview')).toContainText('/opt/env/worker.ts');
    await expect(page.getByTestId('pv-mount-image:config')).toContainText('/opt/env/worker.ts');

    // relative mount path → V_PATH inline
    await mount.fill('config/{{ container.env.NODE_ENV }}.ts');
    await expect(page.getByTestId('vol-config-relative')).toHaveText('must start with / — relative paths are invalid');
    await mount.fill('/etc/core/{{ container.env.NODE_ENV }}.ts');
    await expect(page.getByTestId('vol-config-relative')).toHaveCount(0);

    // expression toggle: a value that is not an option → V_OPTION inline
    await page.getByTestId('env-expr-NODE_ENV').click();
    await page.getByTestId('env-value-NODE_ENV-input').fill('staging');
    await expect(page.getByTestId('ienv-NODE_ENV')).toContainText('is not one of the allowed values of NODE_ENV');
    // back to the list: the value returns to an option (the default)
    await page.getByTestId('env-expr-NODE_ENV').click();
    await expect(page.getByTestId('env-choice-NODE_ENV')).toHaveAttribute('data-value', 'integration');
    await expect(page.getByTestId('ienv-NODE_ENV')).not.toContainText('allowed values');
  });

  test('E_CYCLE between two env vars is shown inline', async ({ page }) => {
    await addCore(page);
    // env[0] = DB_PASSWORD (required secret); extra rows get index 1, 2 (control test ids x1, x2)
    await page.getByTestId('add-env').click();
    await page.getByTestId('env-name-1').fill('A_VAL');
    await page.getByTestId('env-value-x1-input').fill('{{ container.env.B_VAL }}');
    await page.getByTestId('add-env').click();
    await page.getByTestId('env-name-2').fill('B_VAL');
    await page.getByTestId('env-value-x2-input').fill('{{ container.env.A_VAL }}');
    await expect(page.getByTestId('env-value-x1-preview')).toContainText('E_CYCLE');
    await expect(page.getByTestId('env-value-x2-preview')).toContainText('E_CYCLE');
    await expect(page.getByTestId('pv-env-A_VAL')).toContainText('E_CYCLE');
    // breaking the cycle resolves both
    await page.getByTestId('env-value-x2-input').fill('b-{{ container.env.NODE_ENV }}');
    await expect(page.getByTestId('pv-env-B_VAL')).toContainText('b-integration');
    await expect(page.getByTestId('pv-env-A_VAL')).toContainText('b-integration');
  });

  test('route drawer: target service + port dropdowns, header rows, status segmented', async ({ page }) => {
    await login(page, 'admin', `${TPL}/sites/app/edge`);
    await page.getByTestId('add-proxy-route').click();
    await page.waitForURL('**/edge/route?new=proxy');
    await page.getByTestId('route-path').fill('/api/v3');
    await expect(page.getByTestId('route-id')).toHaveValue('api-v3');
    await page.getByTestId('route-service').click();
    const svc = page.getByTestId('route-service-list');
    await expect(svc.locator('[data-value="hello-web"]')).toBeVisible();
    await svc.locator('[data-value="hello-api"]').click();
    await page.getByTestId('route-port').click();
    await expect(page.getByTestId('route-port-list').locator('[data-value="http"]')).toContainText('8080/tcp');
    await page.getByTestId('route-port-list').locator('[data-value="http"]').click();
    await page.getByTestId('method-GET').click();
    await page.getByTestId('add-header').click();
    await pickCombo(page, 'hdr-name-0', 'X-Opm-Site', 'X-Opm-S');
    await page.getByTestId('hdr-value-0-input').fill('{{ self.slug }}');
    await expect(page.getByTestId('hdr-value-0-preview')).toContainText('app');
    await page.getByTestId('route-apply').click();
    await expect(page.getByTestId('route-row-api-v3')).toContainText('hello-api:http');
    await expect(page.getByTestId('route-row-api-v3')).toContainText('GET');
    // switch an existing route to redirect with status 307
    await page.getByTestId('edit-route-api-v3').click();
    await page.waitForURL('**/edge/route?id=api-v3');
    await page.getByTestId('route-kind-redirect').click();
    await page.getByTestId('route-to-input').fill('/v3{path}');
    await page.getByTestId('route-status-307').click();
    await page.getByTestId('route-apply').click();
    await expect(page.getByTestId('route-row-api-v3')).toContainText('307 → /v3{path}');
  });
});

/** Screenshots (OPM_SCREENSHOTS=1): light, dark, 390 px. */
test.describe('config editor screenshots', () => {
  test.skip(!process.env.OPM_SCREENSHOTS, 'set OPM_SCREENSHOTS=1');
  for (const v of [
    { name: 'light', width: 1360, dark: false },
    { name: 'dark', width: 1360, dark: true },
    { name: '390', width: 390, dark: false },
  ]) {
    test(`container + edge + drawers ${v.name}`, async ({ page }) => {
      await page.setViewportSize({ width: v.width, height: v.width < 500 ? 2400 : 1500 });
      if (v.dark) await page.emulateMedia({ colorScheme: 'dark' });
      await addCore(page);
      await page.getByTestId('ienv-NODE_ENV-override').click();
      await pickCombo(page, 'env-choice-NODE_ENV', 'worker');
      await page.getByTestId('add-env').click();
      await page.getByTestId('env-name-2').fill('LOG_TAG');
      await page.getByTestId('env-value-x2-input').fill('core-{{ container.env.NODE_ENV }}');
      await page.getByTestId('port-http-published').click();
      await page.waitForTimeout(300);
      await page.screenshot({ path: `screenshots/ux-mock-config-container-${v.name}.png` });
      await page.goto(`${TPL}/sites/app/edge`);
      await page.getByTestId('edit-route-api').click();
      await expect(page.getByTestId('route-editor')).toBeVisible();
      await page.waitForTimeout(300);
      await page.screenshot({ path: `screenshots/ux-mock-config-route-${v.name}.png` });
      await page.getByTestId('route-cancel').click();
      await page.getByTestId('add-host').click();
      await page.getByTestId('host-tls-acme').click();
      await page.getByTestId('host-challenge-dns-01').click();
      await page.waitForTimeout(300);
      await page.screenshot({ path: `screenshots/ux-mock-config-host-${v.name}.png` });
      await page.keyboard.press('Escape');
      await page.goto(`${TPL}/sites/app/settings`);
      await expect(page.getByTestId('agent-port')).toBeVisible();
      await page.screenshot({ path: `screenshots/ux-mock-config-site-${v.name}.png` });
    });
  }
});
