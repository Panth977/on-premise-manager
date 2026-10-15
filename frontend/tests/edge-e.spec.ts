import { test, expect, type Page } from '@playwright/test';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { login, pickCombo, pick, expectToast, commitAll } from './helpers';

/** Round E (mock): edge route page, Test-a-URL trace, nginx.conf importer. Template "Northwind client (nginx)". */
const TPL = '/app/templates/TplNorthwindClient04';
const EDGE = `${TPL}/sites/site-01/edge`;
const FIXTURE = readFileSync(fileURLToPath(new URL('../../e2e/fixtures/client-nginx.conf', import.meta.url)), 'utf8');
const shots = true; // screenshots/e-mock-*.png are part of round E

async function testUrl(page: Page, url: string, opts: { upgrade?: boolean } = {}) {
  await page.getByTestId('test-url-input').fill(url);
  if (opts.upgrade !== undefined) await page.getByTestId('test-upgrade').setChecked(opts.upgrade);
  await page.getByTestId('test-run').click();
  await expect(page.getByTestId('test-result')).toBeVisible();
}

test.describe('round E: edge editor', () => {
  test('user mistake: Cache-Control as a request header → warning + one-click move to response headers', async ({ page }) => {
    await login(page, 'admin', EDGE);
    await expect(page.getByTestId('route-issues-x-ez-terminal-s3')).toBeVisible();
    await page.getByTestId('edit-route-x-ez-terminal-s3').click();
    await page.waitForURL('**/edge/route?id=x-ez-terminal-s3');
    const warn = page.getByTestId('hdr-warn-0');
    await expect(warn).toBeVisible();
    await expect(warn).toContainText('Cache-Control');
    await expect(page.getByTestId('resp-inherited')).toContainText('no-store');
    if (shots) await page.screenshot({ path: 'screenshots/e-mock-route-warning.png', fullPage: true });
    await page.getByTestId('hdr-move-0').click();
    await expect(page.getByTestId('hdr-warn-0')).toHaveCount(0);
    await expect(page.getByTestId('route-resp-mode-own')).toHaveAttribute('aria-checked', 'true');
    // the route starts from a COPY of the host list (nginx: own list replaces the host's) + Cache-Control set
    await expect(page.getByTestId('route-resp-name-0')).toHaveValue('Cache-Control');
    await expect(page.getByTestId('route-resp-value-0-input')).toHaveValue('public, max-age=3600, must-revalidate');
    await page.getByTestId('route-apply').click();
    await page.waitForURL((u) => u.pathname.endsWith('/edge'));
    await expect(page.getByTestId('route-issues-x-ez-terminal-s3')).toHaveCount(0);
    await expect(page.getByTestId('dirty-dot')).toBeVisible();
    await testUrl(page, 'http://localhost/x/ez-terminal/s3/a.png');
    await expect(page.getByTestId('test-route')).toHaveText('x-ez-terminal-s3');
    await expect(page.getByTestId('test-resp-headers')).toHaveAttribute('data-from', 'route');
    await expect(page.getByTestId('test-resp-set-Cache-Control')).toContainText('public, max-age=3600');
    await expect(page.getByTestId('test-req-headers')).not.toContainText('Cache-Control');
    await commitAll(page);
  });

  test('route page: match kinds, regex check, redirect / rewrite previews, presets, header groups', async ({ page }) => {
    await login(page, 'admin', EDGE);
    // regex redirect (nginx: location ~ ^/apis/(docs)$ { return 301 $uri/$is_args$args; })
    await page.getByTestId('add-redirect-route').click();
    await page.waitForURL('**/edge/route?new=redirect');
    await page.getByTestId('route-match-kind-regex').click();
    await page.getByTestId('route-path').fill('^/apis/(?=docs)');
    await expect(page.locator('#sec-match')).toContainText('lookahead');
    await page.getByTestId('route-path').fill('^/apis/(docs|_docs_/[^/]+)$');
    await expect(page.locator('#sec-match')).not.toContainText('lookahead');
    await page.getByTestId('route-id').fill('apis-docs');
    await page.getByTestId('route-to-input').fill('$uri/$is_args$args');
    await page.getByTestId('route-status-301').click();
    await expect(page.getByTestId('route-keep-path').locator('input')).toBeDisabled();
    await page.getByTestId('route-example').fill('/apis/docs?a=1');
    await expect(page.getByTestId('route-preview-location')).toHaveText('/apis/docs/?a=1');
    await page.getByTestId('route-apply').click();
    await page.waitForURL((u) => u.pathname.endsWith('/edge'));
    await expect(page.getByTestId('route-group-apis-docs')).toHaveText('regex #1');
    await expect(page.getByTestId('up-apis-docs')).toBeVisible();

    // internal rewrite: re-match preview
    await page.getByTestId('add-rewrite-route').click();
    await page.waitForURL('**/edge/route?new=rewrite');
    await expect(page.getByTestId('rewrite-explain')).toContainText('matched again');
    await page.getByTestId('route-path').fill('/api/v0002/device/types');
    await page.getByTestId('route-rw-regex').fill('^');
    await page.getByTestId('route-rw-replacement').fill('/x/ez-terminal$request_uri');
    await page.getByTestId('route-example').fill('/api/v0002/device/types?x=1');
    await expect(page.getByTestId('route-preview-upstream')).toHaveText('/api/v0002/device/types?x=1');
    await expect(page.getByTestId('route-preview-upstream-url')).toContainText('http://ez-terminal:8080/api/v0002/device/types?x=1');
    await expect(page.getByTestId('sec-resp-headers')).toHaveCount(0);
    await page.getByTestId('route-apply').click();
    await page.waitForURL((u) => u.pathname.endsWith('/edge'));

    // proxy route: kinds, path handling, presets, request headers, own response headers
    await page.getByTestId('add-proxy-route').click();
    await page.waitForURL('**/edge/route?new=proxy');
    await page.getByTestId('route-path').fill('/x/ez-terminal/real-time/');
    await pick(page, 'route-service', 'ez-terminal');
    await page.getByTestId('route-path-mode-rewrite').click();
    await expect(page.getByTestId('route-rewrite-regex')).toHaveValue('^/x/ez-terminal/real-time(/.*)$');
    await page.getByTestId('route-rewrite-regex').fill('^/x/ez-terminal(/.*)$');
    await expect(page.getByTestId('route-preview-upstream')).toHaveText('/real-time/example');
    await page.getByTestId('route-path-mode-strip').click();
    await expect(page.getByTestId('route-preview-upstream')).toHaveText('/example');
    await page.getByTestId('route-path-mode-rewrite').click();
    await page.getByTestId('route-rewrite-regex').fill('^/x/ez-terminal(/.*)$');
    await expect(page.getByTestId('route-ws').locator('input')).toBeChecked();
    await page.getByTestId('sec-streaming-toggle').click();
    await page.getByTestId('route-resp-buffering').click();
    await expect(page.getByTestId('route-resp-buffering').locator('input')).toBeChecked();
    await page.getByTestId('preset-sse').click();
    await expect(page.getByTestId('route-resp-buffering').locator('input')).not.toBeChecked();
    await expect(page.getByTestId('route-req-buffering').locator('input')).not.toBeChecked();
    await page.getByTestId('preset-uploads').click();
    await expect(page.getByTestId('route-max-body-mode-custom')).toHaveAttribute('aria-checked', 'true');
    // Uploads preset: 1 GiB body limit (both GUIs, §21 #102)
    await expect(page.getByTestId('route-max-body-num')).toHaveValue('1');
    await expect(page.getByTestId('route-max-body-unit')).toHaveAttribute('data-value', 'GiB');
    await page.getByTestId('route-read-timeout').fill('3600');
    await expect(page.getByTestId('req-defaults')).toContainText('X-Forwarded-Proto');
    await page.getByTestId('add-header').click();
    await page.getByTestId('hdr-name-0').fill('X-Script-Name');
    await page.getByTestId('hdr-value-0-input').fill('/x/ez-terminal');
    await expect(page.getByTestId('hdr-warn-0')).toHaveCount(0);
    await page.getByTestId('add-header').click();
    await page.getByTestId('hdr-name-1').fill('Expires');
    await page.getByTestId('hdr-value-1-input').fill('0');
    await expect(page.getByTestId('hdr-warn-1')).toBeVisible();
    await page.locator('.hrow').nth(1).getByRole('button', { name: /Remove header/ }).click();
    await page.getByTestId('route-resp-mode-own').click();
    await page.getByTestId('route-resp-add').click();
    const last = (await page.locator('[data-testid^="route-resp-row-"]').count()) - 1;
    await page.getByTestId(`route-resp-name-${last}`).fill('X-Accel-Buffering');
    await page.getByTestId(`route-resp-value-${last}-input`).fill('no');
    await page.getByTestId(`route-resp-always-${last}`).check();
    await page.setViewportSize({ width: 1360, height: 2800 });
    if (shots) await page.screenshot({ path: 'screenshots/e-mock-route-editor.png' });
    await page.setViewportSize({ width: 1360, height: 860 });
    await page.getByTestId('route-apply').click();
    await page.waitForURL((u) => u.pathname.endsWith('/edge'));
    await testUrl(page, 'http://localhost/x/ez-terminal/real-time/stream');
    await expect(page.getByTestId('test-route')).toHaveText('x-ez-terminal-real-time');
    await expect(page.getByTestId('test-upstream')).toHaveText('http://ez-terminal:8080/real-time/stream');
    await expect(page.getByTestId('test-timeouts')).toContainText('read 3600 s');
    await expect(page.getByTestId('test-buffering')).toContainText('request streamed');
    await expect(page.getByTestId('test-body')).toContainText('1 GiB');
    await expect(page.getByTestId('test-ws')).toContainText('allowed');
    await expect(page.getByTestId('test-req-X-Script-Name')).toContainText('route');
    await expect(page.getByTestId('test-resp-set-X-Accel-Buffering')).toContainText('always');
  });

  test('importer: paste the client nginx.conf → mapping table → certificate → Replace → effective order → Test a URL', async ({ page }) => {
    await login(page, 'admin', EDGE);
    await page.getByTestId('import-nginx').click();
    await page.waitForURL('**/edge/import');
    await page.getByTestId('ngx-text').fill(FIXTURE);
    await expect(page.getByTestId('ngx-summary')).toContainText('20 routes');
    await expect(page.getByTestId('ngx-count-error')).toContainText('0 error');
    const resolver = page.locator('[data-testid="ngx-row"][data-directive="resolver"]');
    await expect(resolver).toHaveAttribute('data-status', 'ignored');
    await expect(page.locator('[data-testid="ngx-row"][data-line="69"]')).toContainText('responseHeaders.set');
    await page.getByTestId('ngx-filter-ignored').click();
    await expect(page.locator('[data-testid="ngx-row"]')).toHaveCount(4);
    await page.getByTestId('ngx-filter-all').click();
    await page.getByTestId('ngx-summary').evaluate((el) => el.scrollIntoView({ block: 'start' }));
    await page.evaluate(() => window.scrollBy(0, -80));
    if (shots) await page.screenshot({ path: 'screenshots/e-mock-import-mapping.png' });
    await expect(page.getByTestId('ngx-cert-state-0')).toContainText('unbound');
    await pickCombo(page, 'ngx-cert-file-0', 'client-crt');
    await pickCombo(page, 'ngx-cert-key-0', 'client-key');
    await expect(page.getByTestId('ngx-cert-state-0')).toContainText('bound');
    await page.getByTestId('ngx-mode-replace').click();
    await page.getByTestId('ngx-apply').click();
    await page.waitForURL((u) => u.pathname.endsWith('/edge'));
    await expect(page.getByTestId('dirty-dot')).toBeVisible();
    // effective order: the regex first (no exact / ^~), then prefixes longest first, "/" last
    await expect(page.locator('[data-testid^="route-row-"]').first()).toHaveAttribute('data-testid', 'route-row-regex-1');
    await expect(page.locator('[data-testid^="route-row-"]').last()).toHaveAttribute('data-testid', 'route-row-root');
    await expect(page.getByTestId('host-resp-env.client.example.gov.in')).toContainText('3 headers');
    if (shots) await page.screenshot({ path: 'screenshots/e-mock-routes.png', fullPage: true });

    // internal rewrite chain → nodered
    await testUrl(page, 'http://env.client.example.gov.in/api/v0002/device/types?x=1');
    await expect(page.getByTestId('test-rewrites')).toContainText('/x/nodered/api/v0002/device/types?x=1');
    await expect(page.getByTestId('test-route')).toHaveText('x-nodered');
    await expect(page.getByTestId('test-upstream')).toHaveText('http://nodered:1880/api/v0002/device/types?x=1');
    await expect(page.getByTestId('test-order-x-nodered')).toHaveAttribute('data-win', '1');
    if (shots) await page.screenshot({ path: 'screenshots/e-mock-test-rewrite.png', fullPage: true });
    // s3: response headers that reach the browser (route list replaces the host's)
    await testUrl(page, 'http://env.client.example.gov.in/x/ez-terminal/s3/bucket/a.png');
    await expect(page.getByTestId('test-route')).toHaveText('x-ez-terminal-s3');
    await expect(page.getByTestId('test-resp-headers')).toHaveAttribute('data-from', 'route');
    await expect(page.getByTestId('test-resp-set-Cache-Control')).toContainText('public, max-age=3600, must-revalidate');
    await expect(page.getByTestId('test-resp-remove-Pragma')).toBeVisible();
    await expect(page.getByTestId('test-upstream-path')).toContainText('/s3/bucket/a.png');
    // regex 301 with captures
    await testUrl(page, 'http://env.client.example.gov.in/apis/docs');
    await expect(page.getByTestId('test-status')).toHaveText('301');
    await expect(page.getByTestId('test-location')).toHaveText('/apis/docs/');
    await expect(page.getByTestId('test-captures')).toContainText('$1 = “docs”');
    // nginx auto-redirect
    await testUrl(page, 'http://env.client.example.gov.in/x/ez-apis');
    await expect(page.getByTestId('test-status')).toHaveText('301');
    await expect(page.getByTestId('test-auto-redirect')).toBeVisible();
    await expect(page.getByTestId('test-location')).toHaveText('/x/ez-apis/');
    // SSE route (from the import): streamed, 60 s timeouts; host headers on the catch-all
    await testUrl(page, 'http://env.client.example.gov.in/x/ez-terminal/real-time/x');
    await expect(page.getByTestId('test-buffering')).toContainText('request streamed');
    await expect(page.getByTestId('test-buffering')).toContainText('flushed at once');
    await testUrl(page, 'http://env.client.example.gov.in/');
    await expect(page.getByTestId('test-route')).toHaveText('root');
    await expect(page.getByTestId('test-resp-headers')).toHaveAttribute('data-from', 'host');
    await expect(page.getByTestId('test-resp-add-Pragma')).toBeVisible();
    // body limit 100 MiB from the edge (client_max_body_size)
    await page.getByTestId('test-body-on').check();
    await page.getByTestId('test-body-num').fill('200');
    await page.getByTestId('test-run').click();
    await expect(page.getByTestId('test-status')).toHaveText('413');
    await commitAll(page);
  });

  test('importer Upstreams step: containers named like "Single" (northwind-backend-*) → suggestions prefilled, live mapping', async ({ page }) => {
    await login(page, 'admin', `${TPL}/sites/site-02/edge/import`);
    await page.getByTestId('ngx-text').fill(FIXTURE);
    const up = (key: string) => page.locator(`[data-testid^="ngx-up-"][data-key="${key}"]`);
    await expect(page.getByTestId('ngx-upstreams')).toBeVisible();
    // suggestions are applied (prefilled + marked); only the truly missing upstreams stay unresolved
    await expect(up('ez-terminal:8080')).toHaveAttribute('data-state', 'mapped');
    await expect(up('ez-terminal:8080')).toContainText('suggested');
    await expect(up('nodered:1880')).toHaveAttribute('data-state', 'mapped');
    await expect(up('northwind-web:80')).toHaveAttribute('data-state', 'name');
    // northwind-admin:3000: the only "-admin" candidate listens on 8080 → no guess (a fuzzy match needs the same port number)
    for (const k of ['ez-dc:8080', 'ez-pg-listen:8080', 'ez-migrate:8080', 'ez-heatmap:8080', 'pg-admin:80', 'northwind-admin:3000']) await expect(up(k)).toHaveAttribute('data-state', 'unresolved');
    await expect(page.getByTestId('ngx-up-unresolved')).toHaveText('6 unresolved');
    await expect(page.getByTestId('ngx-count-error')).toHaveText('6 error');
    await page.getByTestId('ngx-upstreams').evaluate((el) => el.scrollIntoView({ block: 'start' }));
    await page.screenshot({ path: 'screenshots/e-mock-import-upstreams.png' });
    // clear a suggestion → its 3 routes are unresolved again (mapping updates live)
    const i = await up('ez-terminal:8080').getAttribute('data-testid');
    await page.getByTestId(`ngx-up-clear-${i!.slice('ngx-up-'.length)}`).click();
    await expect(page.getByTestId('ngx-count-error')).toHaveText('9 error');
    // map ez-dc by hand to the worker
    const d = (await up('ez-dc:8080').getAttribute('data-testid'))!.slice('ngx-up-'.length);
    await pick(page, `ngx-up-c-${d}`, 'northwind-backend-worker');
    await expect(up('ez-dc:8080')).toHaveAttribute('data-state', 'mapped');
    await expect(up('ez-dc:8080')).not.toContainText('suggested');
    await expect(page.getByTestId('ngx-count-error')).toHaveText('8 error');
    await page.getByTestId('ngx-apply').click();
    await page.waitForURL((u) => u.pathname.endsWith('/site-02/edge'));
    await expect(page.getByTestId('route-action-x-ez-dc-worker')).toContainText('northwind-backend-worker:http');
    await expect(page.getByTestId('route-action-x-nodered')).toContainText('node-red:http');
  });
});
