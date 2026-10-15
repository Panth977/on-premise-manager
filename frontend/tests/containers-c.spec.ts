import { test, expect, type Page } from '@playwright/test';
import { login, expectToast, SEED, pickCombo, pickSource, pick, commitAll, nav } from './helpers';

/**
 * Round C (SPEC §21 #51–53): optional site host (auto-detect), containers are named instances on a full page
 * (two containers of the same service + version with different env / host ports), duplicate, edge route to one of them.
 */
const TPL = `/app/templates/${SEED.template}`;
const CORE = '/app/templates/TplNorthwindCore0002';

/** add a core-api v2 container named `name` with NODE_ENV = env and a published host port on site app of Hello world */
async function addBackend(page: Page, name: string, env: string, hostPort: number) {
  await nav(page, `${TPL}/sites/app/containers`);
  await page.getByTestId('add-container').click();
  await page.waitForURL(`**${TPL}/sites/app/containers/new`);
  await pickCombo(page, 'c-service', SEED.svcCore, 'core');
  await expect(page.getByTestId('container-version')).toHaveAttribute('data-value', 'v2');
  await page.getByTestId('c-name').fill(name);
  await expect(page.getByTestId('sum-name')).toHaveText(name);
  // round D: image volumes start not mounted; "+ New data volume…" suggests <container>-<volume> and declares it
  for (const v of ['config', 'data']) {
    await pickSource(page, `vol-${v}-source`, 'new-volume');
    await expect(page.getByTestId('vm-slug')).toHaveValue(`${name}-${v}`);
    await page.getByTestId('vm-save').click();
    await expect(page.getByTestId(`vol-${v}-source`)).toHaveAttribute('data-value', `volume:${name}-${v}`);
  }
  // env: NODE_ENV override (choice)
  await page.getByTestId('ienv-NODE_ENV-override').click();
  await pickCombo(page, 'env-choice-NODE_ENV', env);
  await expect(page.getByTestId('pv-env-NODE_ENV')).toContainText(env);
  await expect(page.getByTestId('sum-mounts')).toContainText(`/app/helper/config/env/${env}.ts`);
  // ports: publish http on a host port, bind 127.0.0.1
  await page.getByTestId('port-http-published').click();
  await page.getByTestId('hostport-http-num').fill(String(hostPort));
  await page.getByTestId('port-http-bind-127.0.0.1').click();
  await expect(page.getByTestId('sum-ports')).toContainText(`127.0.0.1:${hostPort} → http`);
  // §21 #83: config is a File image volume → "+ New data volume…" made a single-file volume (read-write): only the rename INFO
  await expect(page.getByTestId('container-issues')).toContainText('V_FILE_VOLUME_RENAME');
  await expect(page.getByTestId('container-issues')).not.toContainText(/warning|error/i);
  await page.getByTestId('apply-container').click();
  await expectToast(page, 'success', /applied|added/);
  await page.waitForURL(`**${TPL}/sites/app/containers`);
  await expect(page.getByTestId(`container-row-${name}`)).toBeVisible();
}

test.describe('round C: site host + container instances', () => {
  test('site host: auto-detect by default, an empty variable shows a hint with "Use auto-detect" (no warning left)', async ({ page }) => {
    await login(page, 'admin', `${CORE}/sites`);
    // list: auto host
    await expect(page.getByTestId('site-host-core')).toHaveText('auto · detected on install');
    await nav(page, `${CORE}/sites/core/settings`);
    const auto = page.getByTestId('site-host-auto').locator('input');
    await expect(auto).toBeChecked();
    await expect(page.getByTestId('site-host-auto-info')).toContainText('auto · detected on install');
    await expect(page.getByTestId('validation-counter')).toContainText('valid');
    // the user's case: host = {{ var.server-ip }} (no value) → hint, never an error
    await page.getByTestId('site-host-auto').click();
    await expect(auto).not.toBeChecked();
    await page.getByTestId('site-host-input').fill('{{ var.server-ip }}');
    await expect(page.getByTestId('site-host-fallback')).toContainText('var server-ip has no value — leave host on auto or set the variable');
    await expect(page.getByTestId('validation-counter')).not.toContainText('error');
    await expect(page.getByTestId('change-bar-commit')).toBeEnabled();
    // one click back to auto: no warning
    await page.getByTestId('use-auto-host').click();
    await expect(auto).toBeChecked();
    await expect(page.getByTestId('site-host-fallback')).toHaveCount(0);
    await expect(page.getByTestId('validation-counter')).toContainText('valid');
    // a real address
    await page.getByTestId('site-host-auto').click();
    await page.getByTestId('site-host-input').fill('10.0.0.40');
    await commitAll(page);
    await nav(page, `${CORE}/sites`);
    await expect(page.getByTestId('site-host-core')).toHaveText('10.0.0.40');
  });

  test('deployment: an auto host of an enrolled site shows the detected address', async ({ page }) => {
    await login(page, 'admin', `/app/deployments/${SEED.deploymentEmpty}/sites`);
    // North grid / main: host null, not installed
    await expect(page.getByTestId('site-host-main')).toHaveText('auto · detected on install');
  });

  test('two containers of the same service + version with different names, env and host ports; duplicate; edge route', async ({ page }) => {
    await login(page, 'admin', `${TPL}/sites/app/containers`);
    await addBackend(page, 'backend-worker', 'worker', 18081);
    await addBackend(page, 'backend-terminal', 'terminal', 18082);
    await commitAll(page, 'Two backend containers');
    const table = page.getByTestId('containers-table');
    await expect(table.getByTestId('container-row-backend-worker')).toContainText('core-api');
    await expect(table.getByTestId('container-row-backend-worker')).toContainText('v2');
    await expect(table.getByTestId('container-row-backend-worker').getByTestId('container-ports')).toContainText('127.0.0.1:18081→http');
    await expect(table.getByTestId('container-row-backend-terminal').getByTestId('container-ports')).toContainText('127.0.0.1:18082→http');

    // reopen: name locked, env kept
    await page.getByTestId('container-row-backend-terminal').getByTestId('container-name').click();
    await page.waitForURL(`**${TPL}/sites/app/containers/backend-terminal`);
    await expect(page.getByTestId('c-name-locked')).toHaveText('backend-terminal');
    await expect(page.getByTestId('env-choice-NODE_ENV')).toHaveAttribute('data-value', 'terminal');
    await expect(page.getByTestId('hostport-http-num')).toHaveValue('18082');

    // the same name twice is refused live
    await nav(page, `${TPL}/sites/app/containers/new?service=${SEED.svcCore}&version=v2`);
    await expect(page.getByTestId('c-name')).toHaveValue('core-api');
    await page.getByTestId('c-name').fill('backend-worker');
    await expect(page.getByTestId('container-editor').locator('..')).toContainText('a container named backend-worker already exists on site app');
    await expect(page.getByTestId('apply-container')).toBeDisabled();
    await page.getByTestId('c-name').fill('new');
    await expect(page.getByTestId('apply-container')).toBeDisabled();
    await page.getByTestId('cancel-container').click();
    await page.waitForURL(`**${TPL}/sites/app/containers`);

    // duplicate: same image + env, host ports cleared, own data volumes renamed, name suggested
    await page.getByTestId('dup-container-backend-worker').click();
    await page.waitForURL(/containers\/new\?duplicate=backend-worker/);
    await expect(page.getByTestId('c-name')).toHaveValue('core-api');
    await expect(page.getByTestId('env-choice-NODE_ENV')).toHaveAttribute('data-value', 'worker');
    await expect(page.getByTestId('port-http-published').locator('input')).not.toBeChecked();
    await page.getByTestId('c-name').fill('backend-worker-2');
    // the copy's own volumes (declared for the copy) follow the name
    await expect(page.getByTestId('vol-data-source')).toHaveAttribute('data-value', 'volume:backend-worker-2-data');
    // share the data volume of backend-worker
    await pickSource(page, 'vol-data-source', 'volume:backend-worker-data');
    // shared container form (§21 #102): compact "shared · N containers" badge + list
    await expect(page.getByTestId('vol-data-shared')).toContainText('shared · 2 containers');
    await expect(page.getByTestId('vol-data-shared-list')).toContainText('backend-worker');
    await page.getByTestId('apply-container').click();
    await expectToast(page, 'success', /applied|added/);
    await expect(page.getByTestId('container-row-backend-worker-2')).toBeVisible();

    // edge route to the SECOND container (backend-terminal); a port with "edge may proxy" off is disabled
    await nav(page, `${TPL}/sites/app/containers/backend-worker`);
    await page.getByTestId('port-http-edge').click();
    await page.getByTestId('apply-container').click();
    await expectToast(page, 'success', /applied|added/);
    await nav(page, `${TPL}/sites/app/edge`);
    await page.getByTestId('add-proxy-route').click();
    await page.waitForURL('**/edge/route?new=proxy');
    await page.getByTestId('route-path').fill('/terminal');
    await page.getByTestId('route-service').click();
    const list = page.getByTestId('route-service-list');
    await expect(list.locator('[data-value="backend-terminal"]')).toContainText('core-api:v2');
    await list.locator('[data-value="backend-worker"]').click();
    await page.getByTestId('route-port').click();
    await expect(page.getByTestId('route-port-list').locator('[data-value="http"]')).toHaveAttribute('aria-disabled', 'true');
    await page.keyboard.press('Escape');
    await pick(page, 'route-service', 'backend-terminal');
    await page.getByTestId('route-port').click();
    await expect(page.getByTestId('route-port-list').locator('[data-value="http"]')).not.toHaveAttribute('aria-disabled', 'true');
    await page.getByTestId('route-port-list').locator('[data-value="http"]').click();
    await page.getByTestId('route-apply').click();
    await expect(page.getByTestId('route-row-terminal')).toContainText('backend-terminal:http');
    await commitAll(page);
  });

  test('old links …/containers/{serviceSlug} redirect when one container runs that service', async ({ page }) => {
    await login(page, 'admin', `${CORE}/sites/core`);
    // core-api runs twice → no redirect
    await page.goto(`${CORE}/sites/core/containers/core-api-old`);
    await expect(page.locator('body')).toContainText('No container core-api-old on core');
    // redis runs once (container "cache") → the old service link redirects
    await page.goto(`${CORE}/sites/core/containers/redis`);
    await page.waitForURL(`**${CORE}/sites/core/containers/cache`);
    await expect(page.getByTestId('c-name-locked')).toHaveText('cache');
    // Hello world app: hello-web is the container name too → opens directly
    await page.goto(`${TPL}/sites/app/containers/hello-web`);
    await expect(page.getByTestId('c-name-locked')).toHaveText('hello-web');
  });
});
