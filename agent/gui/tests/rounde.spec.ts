import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { test, expect } from './fixtures';
import { login, shot, pickOption } from './helpers';

/** Round E (edge nginx parity) on the mock site "northwind" = e2e/fixtures/client-nginx.conf written in the round E shape. */
const FIXTURE = readFileSync(fileURLToPath(new URL('../../../e2e/fixtures/client-nginx.conf', import.meta.url)), 'utf8');
const H = 'https://env.client.example.gov.in';

/** The edge forms are the shared $opm forms (SPEC §21 #102): the same test ids as the cloud console. */
test('route editor: the Cache-Control mistake is flagged and fixed with one click (move to response headers)', async ({ page }) => {
  await login(page, '/_opm/app/northwind/config/edge');
  const row = page.getByTestId('route-row-x-ez-terminal-s3');
  await expect(row).toContainText('1 warning');
  await shot(page, 'e-edge-page');
  await page.getByTestId('edit-route-x-ez-terminal-s3').click();
  await page.waitForURL(/config\/edge\/route\?id=x-ez-terminal-s3$/);
  const req = page.getByTestId('sec-req-headers');
  await expect(req.getByTestId('hdr-warn-0')).toContainText('W_RESPONSE_HEADER_AS_REQUEST');
  await expect(page.getByTestId('route-resp-mode-inherit')).toHaveAttribute('aria-checked', 'true');
  await shot(page, 'e-route-mistake');
  await req.getByTestId('hdr-move-0').click();
  await expect(req.getByTestId('hdr-warn-0')).toHaveCount(0);
  await expect(page.getByTestId('route-resp-mode-own')).toHaveAttribute('aria-checked', 'true');
  // set Cache-Control (moved), the host's Pragma / Expires kept as add (copy of the host list)
  const resp = page.getByTestId('route-resp');
  await expect(resp.getByTestId('route-resp-name-0')).toHaveValue('Cache-Control');
  await expect(resp.getByTestId('route-resp-value-0-input')).toHaveValue('public, max-age=3600, must-revalidate');
  await expect(resp.getByTestId('route-resp-mode-0-set')).toHaveAttribute('aria-checked', 'true');
  await expect(resp.getByTestId('route-resp-name-1')).toHaveValue('Pragma');
  await expect(page.getByTestId('route-preview-upstream-url')).toContainText('http://northwind-backend-terminal:8080/s3/example');
  await shot(page, 'e-route-fixed');
  await page.getByTestId('route-apply').click();
  await page.waitForURL(/config\/edge$/);
  await expect(page.getByTestId('route-row-x-ez-terminal-s3')).not.toContainText('warning');
  await expect(page.getByTestId('local-edit-bar')).toContainText('(not committed)');
});

test('route editor: match kinds, regex check, actions, path handling preview, streaming presets, header groups', async ({ page }) => {
  await login(page, '/_opm/app/northwind/config/edge');
  await page.getByTestId('add-proxy').click();
  await page.waitForURL(/route\?new=proxy$/);
  const ed = page.getByTestId('route-editor');
  await ed.getByTestId('route-path').fill('/x/live/');
  await ed.getByTestId('route-id').fill('live');
  await pickOption(page, 'route-service', 'northwind-backend-terminal');
  await expect(ed.getByTestId('route-port')).toHaveAttribute('data-value', 'http');
  // path handling: keep → strip → rewrite, live preview of what the container gets
  await expect(page.getByTestId('route-preview-upstream')).toHaveText('/x/live/example');
  await ed.getByTestId('route-path-mode-strip').click();
  await expect(page.getByTestId('route-preview-upstream')).toHaveText('/example');
  await ed.getByTestId('route-path-mode-rewrite').click();
  await expect(ed.getByTestId('route-rewrite-regex')).toHaveValue('^/x/live(/.*)$');
  await expect(page.getByTestId('route-preview-upstream')).toHaveText('/example');
  await ed.getByTestId('route-add-prefix').fill('/v2');
  await expect(page.getByTestId('route-preview-upstream')).toHaveText('/v2/example');
  // streaming presets
  await ed.getByTestId('sec-streaming-toggle').click();
  await ed.getByTestId('route-req-buffering').check();
  await ed.getByTestId('route-resp-buffering').check();
  await ed.getByTestId('preset-sse').click();
  await expect(ed.getByTestId('route-req-buffering').locator('input')).not.toBeChecked();
  await expect(ed.getByTestId('route-resp-buffering').locator('input')).not.toBeChecked();
  await ed.getByTestId('preset-uploads').click();
  await expect(ed.getByTestId('route-max-body-mode-custom')).toHaveAttribute('aria-checked', 'true');
  await expect(ed.getByTestId('route-max-body-unit')).toHaveAttribute('data-value', 'GiB');
  await expect(ed.getByTestId('route-ws').locator('input')).toBeChecked();
  await page.getByRole('button', { name: 'Increase Read timeout' }).click();
  await expect(ed.getByTestId('route-read-timeout')).toHaveValue('65');
  // header groups
  await ed.getByTestId('add-header').click();
  await ed.getByTestId('hdr-name-0').fill('X-Forwarded-Prefix');
  await ed.getByTestId('hdr-value-0-input').fill('/x/live');
  await ed.getByTestId('route-resp-mode-own').click();
  await ed.getByTestId('route-resp-add').click();
  const last = (await ed.locator('[data-testid^="route-resp-row-"]').count()) - 1;
  await ed.getByTestId(`route-resp-name-${last}`).fill('X-Accel-Buffering');
  await ed.getByTestId(`route-resp-value-${last}-input`).fill('no');
  await shot(page, 'e-route-proxy');

  // match kinds: regex with a live RE2 ∩ JS check
  await ed.getByTestId('route-match-kind-regex').click();
  await expect(ed.getByTestId('route-path-mode-strip')).toBeDisabled();
  await ed.getByTestId('route-path').fill('^/x/(?=live)');
  await expect(page.locator('#sec-match')).toContainText('lookahead');
  await page.getByTestId('route-apply').click();
  await expect(page).toHaveURL(/route\?new=proxy$/);
  await ed.getByTestId('route-path').fill('^/x/live/(.*)$');
  await expect(page.locator('#sec-match')).not.toContainText('lookahead');
  await ed.getByTestId('regex-ci').check();
  await expect(ed.getByTestId('route-path')).toHaveValue('(?i)^/x/live/(.*)$');
  // redirect: 303, keep path only for prefix
  await ed.getByTestId('route-kind-redirect').click();
  await expect(ed.getByTestId('route-vars')).toContainText('$request_uri');
  await expect(ed.getByTestId('route-keep-path').locator('input')).toBeDisabled();
  await ed.getByTestId('route-to-input').fill('https://$host/new/$1');
  await ed.getByTestId('route-status-303').click();
  await page.getByTestId('route-example').fill('/x/live/abc');
  await expect(page.getByTestId('route-preview-location')).toHaveText('https://env.client.example.gov.in/new/abc');
  await shot(page, 'e-route-redirect');
  // rewrite: internal, re-matched; no response headers group
  await ed.getByTestId('route-kind-rewrite').click();
  await expect(ed.getByTestId('rewrite-explain')).toContainText('matched again');
  await expect(ed.getByTestId('sec-resp-headers')).toHaveCount(0);
  await ed.getByTestId('route-rw-regex').fill('^/x/live/(.*)$');
  await ed.getByTestId('route-rw-replacement').fill('/x/nodered/$1');
  await expect(page.getByTestId('route-preview-rewrites')).toContainText('lands on x-nodered');
  await expect(page.getByTestId('route-preview-upstream-url')).toContainText('http://nodered:1880/abc');
  await shot(page, 'e-route-rewrite');
  // back to proxy: the proxy settings come back
  await ed.getByTestId('route-kind-proxy').click();
  await expect(ed.getByTestId('route-add-prefix')).toHaveValue('/v2');
  await page.getByTestId('route-apply').click();
  await page.waitForURL(/config\/edge$/);
  await expect(page.getByTestId('route-row-live')).toHaveAttribute('data-group', 'regex');
  // regex routes can move; others cannot
  await expect(page.getByTestId('route-row-live').getByRole('button', { name: 'Move live up' })).toBeEnabled();
  await expect(page.getByTestId('route-row-root').getByRole('button', { name: /Move/ })).toHaveCount(0);
  await page.getByTestId('route-row-live').getByRole('button', { name: 'Move live up' }).click();
  await expect(page.getByTestId('routes').locator('tbody tr').first()).toHaveAttribute('data-testid', 'route-row-live');
});

test('Test a URL: s3 response headers, internal rewrite chain to nodered, regex 301, auto-redirect, 413, websocket', async ({ page }) => {
  await login(page, '/_opm/app/northwind/config/edge');
  const url = page.getByTestId('test-url-input');
  const go = page.getByTestId('test-run');
  const res = page.getByTestId('test-result');
  // s3: the route's own list replaces the host's no-cache headers
  await url.fill(`${H}/x/ez-admin/s3/bucket/logo.png`);
  await go.click();
  await expect(res.getByTestId('test-upstream')).toHaveText('http://northwind-backend-admin:8080/s3/bucket/logo.png');
  const rh = res.getByTestId('test-resp-headers');
  await expect(rh).toHaveAttribute('data-from', 'route');
  await expect(rh).toContainText('public, max-age=3600, must-revalidate');
  await expect(rh).toContainText('Pragma');
  await expect(rh).not.toContainText('no-store');
  await expect(res.getByTestId('test-req-headers')).toContainText('X-Forwarded-Proto');
  await expect(res.getByTestId('test-body')).toContainText('100 MiB');
  await shot(page, 'e-test-s3');
  // a normal route inherits the host's no-cache headers
  await url.fill(`${H}/x/ez-apis/v1/devices`);
  await go.click();
  await expect(res.getByTestId('test-resp-headers')).toHaveAttribute('data-from', 'host');
  await expect(res.getByTestId('test-resp-headers')).toContainText('no-store');
  // internal rewrite (nginx "last") → re-matched → nodered
  await url.fill(`${H}/api/v0002/device/types?org=7`);
  await go.click();
  await expect(res.getByTestId('test-rewrites')).toContainText('/api/v0002/device/types?org=7');
  await expect(res.getByTestId('test-rewrites')).toContainText('/x/nodered/api/v0002/device/types?org=7');
  await expect(res.getByTestId('test-route')).toHaveText('x-nodered');
  await expect(res.getByTestId('test-upstream')).toHaveText('http://nodered:1880/api/v0002/device/types?org=7');
  await shot(page, 'e-test-rewrite');
  // regex redirect 301 with captures
  await url.fill(`${H}/apis/docs`);
  await go.click();
  await expect(res.getByTestId('test-status')).toHaveText('301');
  await expect(res.getByTestId('test-location')).toHaveText('/apis/docs/');
  await expect(res.getByTestId('test-captures')).toContainText('docs');
  // nginx auto-redirect
  await url.fill(`${H}/x/ez-apis`);
  await go.click();
  await expect(res.getByTestId('test-auto-redirect')).toBeVisible();
  await expect(res.getByTestId('test-location')).toHaveText('/x/ez-apis/');
  await shot(page, 'e-test-auto-redirect');
  // 413 above the edge limit, websocket allowed
  await url.fill(`${H}/x/ez-worker/upload`);
  await page.getByTestId('test-body-on').check();
  await page.getByTestId('test-body-num').fill('200');
  await page.getByTestId('test-upgrade').check();
  await go.click();
  await expect(res.getByTestId('test-status')).toHaveText('413');
  await page.getByTestId('test-body-on').uncheck();
  await go.click();
  await expect(res.getByTestId('test-status')).toHaveText('200');
  await expect(res.getByTestId('test-ws')).toContainText('allowed');
});

test('importer: paste the client nginx.conf → rows → bind the certificate → Replace → routes in effective order', async ({ page }) => {
  await login(page, '/_opm/app/northwind/config/edge');
  await page.getByTestId('import-nginx').click();
  await page.waitForURL(/config\/edge\/import$/);
  await page.getByTestId('ngx-text').fill(FIXTURE);
  await expect(page.getByTestId('ngx-summary')).toContainText('20 routes');
  const rows = page.getByTestId('ngx-mapping');
  await expect(rows.locator('tbody tr').first()).toBeVisible();
  // filter: only ignored rows (worker_processes, events, resolver …)
  await page.getByTestId('ngx-filter-ignored').click();
  await expect(rows.locator('tbody tr[data-status="ok"]')).toHaveCount(0);
  await expect(rows).toContainText('worker_processes');
  await page.getByTestId('ngx-filter-all').click();
  // Upstreams: the nginx.conf says ez-terminal, this site runs northwind-backend-terminal → suggested, prefilled
  const up = (key: string) => page.locator(`[data-testid^="ngx-up-"][data-key="${key}"]`);
  await expect(up('ez-terminal:8080')).toHaveAttribute('data-state', 'mapped');
  await expect(up('ez-terminal:8080')).toContainText('suggested');
  await expect(up('nodered:1880')).toHaveAttribute('data-state', 'name');
  await expect(page.getByTestId('ngx-up-unresolved')).toHaveText('all resolved');
  await expect(page.getByTestId('ngx-count-error')).toHaveText('0 error');
  await shot(page, 'e-import-upstreams');
  // clear one → unresolved, the mapping table follows live; pick it again
  const h = (await up('ez-heatmap:8080').getAttribute('data-testid'))!.slice('ngx-up-'.length);
  await page.getByTestId(`ngx-up-clear-${h}`).click();
  await expect(up('ez-heatmap:8080')).toHaveAttribute('data-state', 'unresolved');
  await expect(page.getByTestId('ngx-up-unresolved')).toHaveText('1 unresolved');
  await expect(page.getByTestId('ngx-count-error')).not.toHaveText('0 error');
  await pickOption(page, `ngx-up-c-${h}`, 'northwind-backend-heatmap');
  await expect(up('ez-heatmap:8080')).toHaveAttribute('data-state', 'mapped');
  await expect(page.getByTestId('ngx-count-error')).toHaveText('0 error');
  await expect(page.getByTestId('ngx-route-x-ez-heatmap')).toContainText('northwind-backend-heatmap:http');
  // the nodered rewrite lands on x-nodered → nodered (not the web container)
  await expect(page.getByTestId('ngx-result')).toContainText('nodered');
  await shot(page, 'e-import-rows');
  // certificate binding
  const cert = page.getByTestId('ngx-cert-0');
  await expect(page.getByTestId('ngx-certs')).toContainText('/etc/nginx/certs/client.crt');
  await cert.getByTestId('ngx-cert-file-0').click();
  await page.getByTestId('ngx-cert-file-0-list').locator('[role="option"][data-value="client-cert"]').click();
  await cert.getByTestId('ngx-cert-key-0').click();
  await page.getByTestId('ngx-cert-key-0-list').locator('[role="option"][data-value="client-key"]').click();
  await expect(page.getByTestId('ngx-cert-state-0')).toContainText('bound');
  await page.getByTestId('ngx-mode-replace').click();
  await shot(page, 'e-import-bind');
  await page.getByTestId('ngx-apply').click();
  await page.getByRole('button', { name: 'Replace', exact: true }).click();
  await page.waitForURL(/config\/edge$/);
  const order = page.getByTestId('routes').locator('tbody tr');
  await expect(order.first()).toHaveAttribute('data-group', 'regex');
  await expect(order.last()).toContainText('/');
  await expect(page.getByTestId('hosts')).toContainText('cert client-cert · key client-key');
  await expect(page.getByTestId('route-row-x-ez-terminal')).toContainText('northwind-backend-terminal');
  await shot(page, 'e-import-done');
});
