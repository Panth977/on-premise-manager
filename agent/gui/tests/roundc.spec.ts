import { test, expect } from './fixtures';
import { login, open, shot, pickOption } from './helpers';

// Round C (SPEC §21 #51–54): containers are instances (containerSlug), optional site host (auto), template files.

test('two instances of the same image: list, local edit of the second one (env), image picked, name fixed', async ({ page }) => {
  await login(page, '/_opm/app/app/config');
  const rows = page.getByTestId('containers');
  await expect(page.getByTestId('container-row-hello-api')).toContainText('hello-api:v1');
  await expect(page.getByTestId('container-row-hello-api-2')).toContainText('hello-api:v1');
  await expect(page.getByTestId('container-row-hello-api-2')).toContainText('http→9081');
  // §21 #113 a site is standalone: add / duplicate / remove containers here
  await expect(page.getByTestId('add-container')).toBeEnabled();
  await expect(rows.getByRole('button', { name: 'Remove hello-api-2' })).toBeEnabled();
  await shot(page, 'c-containers');

  await rows.getByRole('link', { name: 'hello-api-2', exact: true }).click();
  await page.waitForURL(/config\/containers\/hello-api-2$/);
  // §21 #113 the image is picked with "Change image…" (not typed); the name is fixed after create (§21 #102 lock + reason)
  for (const [id, v, why] of [
    ['c-name-locked', 'hello-api-2', /Fixed after create/],
    ['c-service-locked', 'hello-api', /Change image/],
    ['c-version-locked', 'v1', /Change image/],
  ] as const) {
    await expect(page.getByTestId(id)).toHaveText(v);
    await expect(page.getByTestId(id)).toHaveAttribute('aria-readonly', 'true');
    await expect(page.getByTestId(id)).toHaveAttribute('title', why);
  }
  await expect(page.getByTestId('sec-service').getByTestId('field-lock')).toHaveCount(3);
  await expect(page.getByTestId('change-image')).toBeEnabled();
  await expect(page.getByRole('button', { name: 'Duplicate' })).toBeEnabled();
  // its own config: env NODE_ENV=dc, local-only port, own data volume, rendered JSON file
  await expect(page.getByTestId('env-choice-NODE_ENV')).toHaveAttribute('data-value', 'dc');
  await expect(page.getByTestId('port-http-bind').getByRole('radio', { name: 'local (127.0.0.1)' })).toHaveAttribute('aria-checked', 'true');
  await expect(page.getByTestId('port-http-edge-input')).not.toBeChecked();
  await expect(page.getByTestId('vol-data-source')).toHaveAttribute('data-value', 'volume:hello-api-2-data');
  await expect(page.getByTestId('container-summary')).toContainText('127.0.0.1:9081 → http');
  await expect(page.getByTestId('resolved-preview')).toContainText('/app/helper/config/env/dc.ts');

  // local edit of the second instance's env
  await pickOption(page, 'env-choice-NODE_ENV', 'migrate');
  await expect(page.getByTestId('resolved-preview')).toContainText('/app/helper/config/env/migrate.ts');
  await expect(page.getByTestId('local-edit-bar')).toContainText('1 change (not committed)');
  await expect(page.getByTestId('change-bar-commit')).toBeEnabled();

  // preview of the template file: rendered ONCE for the site (round D part 2), secrets masked
  await page.getByTestId('extra-row-2').getByRole('button', { name: 'Preview' }).click();
  const pv = page.getByTestId('file-preview-text');
  await expect(pv).toContainText('"site": "app"');
  await expect(pv).toContainText('"theme": "green"');
  await expect(pv).not.toContainText('Zx8Kq2');
  await shot(page, 'c-instance-preview', false);
  await page.keyboard.press('Escape');

  // the first instance is untouched (in-app navigation: the working copy lives in this tab's memory)
  await page.getByTestId('nav-containers').click();
  await expect(page.getByTestId('container-row-hello-api-2')).toContainText('edited');
  await page.getByTestId('containers').getByRole('link', { name: 'hello-api', exact: true }).click();
  await expect(page.getByTestId('c-name-locked')).toHaveText('hello-api');
  await expect(page.getByTestId('resolved-preview')).toContainText('/app/helper/config/env/worker.ts');
  await page.getByTestId('nav-containers').click();
  await expect(page.getByTestId('container-row-hello-api')).not.toContainText('edited');
  await page.getByTestId('review-changes').click();
  await expect(page.getByTestId('review-drawer').getByTestId('diff')).toContainText('hello-api-2');
  await shot(page, 'c-review');
});

test('volumes: grouped source dropdown (round D), extra mount; ports: bind + edge', async ({ page }) => {
  await login(page, '/_opm/app/app/config/containers/hello-api');
  const row = page.getByTestId('vol-row-data');
  const src = page.getByTestId('vol-data-source');
  await pickOption(page, 'vol-data-source', 'host');
  await row.getByLabel('Host path of data').fill('srv/x');
  await expect(row.getByTestId('vol-data-issue').first()).toContainText('V_VOLUME');
  await row.getByLabel('Host path of data').fill('/srv/hello');
  await expect(row.getByTestId('vol-data-issue')).toHaveCount(0);
  // share the second instance's data volume
  await src.click();
  await expect(page.getByTestId('vol-data-source-opt-volume:hello-api-2-data')).toContainText('shared with hello-api-2');
  await page.getByTestId('vol-data-source-opt-volume:hello-api-2-data').click();
  await expect(src).toHaveAttribute('data-value', 'volume:hello-api-2-data');
  // the env-config image volume: not mounted
  await pickOption(page, 'vol-env-config-source', 'none');
  await expect(page.getByTestId('vol-row-env-config')).toContainText('container storage only');
  // extra mount (secret → read-only badge, no toggle)
  await page.getByTestId('add-mount').click();
  const extra = page.locator('[data-testid^="extra-row-"]').last();
  await pickOption(page, (await extra.getByRole('combobox', { name: 'Source of the extra mount' }).getAttribute('data-testid'))!, 'secret:api-token');
  await expect(extra).toContainText('read-only');
  await expect(extra.getByRole('switch', { name: /Read-only/ })).toHaveCount(0);
  await expect(page.getByTestId('change-bar-commit')).toBeDisabled(); // extra mount needs a mount path
  await extra.getByTestId(/extra-\d+-mount-input/).fill('/run/secrets/token');
  // ports: publish + bind local + edge off
  // shared container form (§21 #102): Published toggle + host port
  if (!(await page.getByTestId('hostport-http-num').count())) await page.getByTestId('port-http-published').click();
  await page.getByTestId('hostport-http-num').fill('9090');
  await page.getByTestId('port-http-bind').getByRole('radio', { name: 'local (127.0.0.1)' }).click();
  await expect(page.getByTestId('container-summary')).toContainText('127.0.0.1:9090 → http');
  await page.getByTestId('port-http-edge').uncheck();
  // the edge still routes /api to hello-api:http → V_EDGE_ROUTE
  await expect(page.getByTestId('local-edit-bar')).toContainText('error');
  await page.getByTestId('port-http-edge').check();
  await expect(page.getByTestId('change-bar-commit')).toBeEnabled();
  // health check override
  await page.getByTestId('hc-override').check();
  await page.getByTestId('hc-type').getByRole('radio', { name: 'tcp' }).click();
  await expect(page.getByTestId('hc')).toContainText('Interval s');
  await shot(page, 'c-volumes-ports');
});

test('site host auto: "auto · <advertised address>", Auto-detect toggle, sites cards + tiles', async ({ page }) => {
  await login(page, '/_opm/app/db/config?tab=site');
  await expect(page.getByTestId('site-host-auto-input')).toBeChecked();
  await expect(page.getByTestId('site-host-detected')).toHaveText('10.0.1.11');
  await page.getByTestId('site-host-auto-input').uncheck();
  const host = page.locator('#sh-host');
  await expect(host).toBeVisible();
  await host.fill('db.acme.internal');
  await expect(page.getByTestId('local-edit-bar')).toContainText('1 change (not committed)');
  await page.getByTestId('site-host-auto-input').check();
  await expect(page.getByTestId('site-host-detected')).toHaveText('10.0.1.11');
  await expect(page.getByTestId('review-changes')).toBeDisabled();
  await shot(page, 'c-site-auto');
  await open(page, '/_opm/app/sites');
  await expect(page.getByTestId('site-host-db')).toHaveText('auto · 10.0.1.11');
  await open(page, '/_opm/app/*/config');
  await expect(page.getByTestId('auto-host-db')).toHaveText('auto · 10.0.1.11');
  await open(page, '/_opm/app');
  await expect(page.getByTestId('tile-host-db')).toContainText('auto · 10.0.1.11');
});

test('files: render mode shown, live {{ }} check, preview as container', async ({ page }) => {
  await login(page, '/_opm/app/files');
  await expect(page.getByTestId('render-api-config')).toHaveText('expressions');
  await page.getByTestId('edit-api-config').click();
  const ed = page.getByTestId('file-drawer').getByTestId('code-editor');
  await expect(ed).toHaveValue(/self\.slug/);
  await page.getByTestId('preview-as').click();
  await expect(page.getByTestId('file-render-preview')).toContainText('"site": "app"');
  await expect(page.getByTestId('file-render-preview')).toContainText('"token": "');
  await expect(page.getByTestId('file-render-preview')).not.toContainText('Zx8Kq2');
  // round D part 2: files are shared by all containers of a site → container.* is not allowed
  await ed.fill('{ "x": "{{ container.env.NODE_ENV }}" }');
  await expect(page.getByTestId('file-drawer')).toContainText('E_NOT_ALLOWED');
  await ed.fill('{ "x": "{{ var.nope }}" }');
  await expect(page.getByTestId('file-drawer')).toContainText('E_UNKNOWN_PATH');
  await ed.fill('{ "x": {{ "{{" }} }');
  await expect(page.getByTestId('file-drawer')).toContainText('V_FILE_JSON');
  await shot(page, 'c-file-preview', false);
});

test('status: Name = containerSlug + Service + Image; logs per container', async ({ page }) => {
  await login(page, '/_opm/app/app/status');
  const t = page.getByTestId('containers-status');
  await expect(page.getByTestId('status-name-hello-api')).toBeVisible();
  await expect(page.getByTestId('status-name-hello-api-2')).toBeVisible();
  await expect(t.locator('tr', { has: page.getByTestId('status-name-hello-api-2') })).toContainText('hello-api:v1');
  await pickOption(page, 'logs-container', 'hello-api-2 (hello-api:v1)');
  await expect(page.getByTestId('logs')).toContainText('hello-api-2 GET');
  await shot(page, 'c-status');
});
