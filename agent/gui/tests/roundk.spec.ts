/**
 * Round K (§21 #93): ANSI colors in every log view + implicit / suggested depends, on the mock agent.
 * Mock: northwind-backend-admin logs = Deno colored output (ESC kept); node-red = a stream that lost its ESC bytes;
 * northwind-backend-admin env MQTT_URL=mqtt://mqtt:1883 (suggested) + NODERED_URL={{ containers.nodered.host }} (implicit).
 */
import { test, expect } from './fixtures';
import { login, pickOption, shot } from './helpers';

const RED = 'rgb(248, 113, 113)'; // --ansi-1

test('tools logs: Deno ANSI → styled spans, Colors off → plain, Raw → ␛, search on the visible text', async ({ page }) => {
  await login(page, '/_opm/app/northwind/containers/northwind-backend-admin?tab=logs');
  const lines = page.getByTestId('log-lines');
  await expect(lines).toContainText('error: Uncaught (in promise) Error: getaddrinfo EAI_AGAIN mqtt');
  await expect(lines).not.toContainText('[0m');
  await expect(lines).not.toContainText('[31m');
  await expect(lines).not.toContainText('deno: admin'); // OSC title removed
  const err = lines.locator('.tx span', { hasText: /^error$/ }).first();
  await expect(err).toHaveAttribute('style', /var\(--ansi-1/);
  await expect(err).toHaveAttribute('style', /font-weight:600/);
  await expect(err).toHaveCSS('color', RED);
  // dim 256-color 245 "at": explicit gray, readable (no opacity)
  const at = lines.locator('.tx span', { hasText: /^at $/ }).first();
  await expect(at).toHaveCSS('color', 'rgb(138, 138, 138)');
  await expect(at).toHaveCSS('opacity', '1');
  await shot(page, 'k-logs-ansi-dark-bg', false);

  // search matches the visible text, the highlight sits inside the styled spans
  await page.getByTestId('log-search').fill('EAI_AGAIN mqtt');
  await expect(page.getByTestId('log-matches')).not.toHaveText('0');
  await expect(lines.locator('mark').first()).toHaveText('EAI_AGAIN mqtt');
  await page.getByTestId('log-search').fill('');

  // Colors off → plain text, no styled spans
  await page.getByTestId('log-ansi-colors').uncheck();
  await expect(lines.locator('.tx span[style]')).toHaveCount(0);
  await expect(lines).toContainText('error: Uncaught (in promise)');
  await expect(lines).not.toContainText('[0m');
  // Raw → the bytes, ESC shown as ␛
  await page.getByTestId('log-ansi-raw').check();
  await expect(lines).toContainText('␛[0m␛[1m␛[31merror␛[0m');
  await page.getByTestId('log-ansi-raw').uncheck();
  await page.getByTestId('log-ansi-colors').check();
  await expect(lines.locator('.tx span', { hasText: /^error$/ }).first()).toHaveCSS('color', RED);
});

test('tools logs: an ESC-lost stream renders styled and keeps "[1] item"', async ({ page }) => {
  await login(page, '/_opm/app/northwind/containers/node-red?tab=logs');
  const lines = page.getByTestId('log-lines');
  await expect(lines).toContainText('[1] item queued for retry');
  await expect(lines).toContainText('error: flow "Device types" failed');
  await expect(lines).not.toContainText('[0m');
  await expect(lines.locator('.tx span', { hasText: /^error$/ }).first()).toHaveCSS('color', RED);
});

test('status page container logs: ANSI colors + toggles', async ({ page }) => {
  await login(page, '/_opm/app/northwind/status');
  await pickOption(page, 'logs-container', 'northwind|northwind-backend-admin');
  const logs = page.getByTestId('logs');
  await expect(logs).toContainText('Uncaught (in promise) Error: getaddrinfo EAI_AGAIN mqtt');
  await expect(logs).not.toContainText('[0m');
  await expect(logs.locator('span', { hasText: /^error$/ }).first()).toHaveCSS('color', RED);
  await page.getByTestId('logs-ansi-raw').check();
  await expect(logs).toContainText('␛[31merror');
});

test('activity update.failed: hint above the colored log tail', async ({ page }) => {
  await login(page, '/_opm/app/northwind/activities');
  await page.getByTestId('activities').getByText('Update failed in apply on northwind').first().click();
  const d = page.getByTestId('activity-drawer');
  await expect(d.getByTestId('activity-hint-northwind-backend-admin')).toContainText('northwind-backend-admin could not resolve `mqtt` (EAI_AGAIN) — is mqtt running/healthy? add it to Depends on');
  const tail = d.getByTestId('activity-logs-northwind-backend-admin');
  await expect(tail).toContainText('error: Uncaught (in promise)');
  await expect(tail).not.toContainText('[0m');
  await expect(tail.locator('span', { hasText: /^error$/ }).first()).toHaveCSS('color', RED);
  await d.getByTestId('activity-failure-northwind-backend-admin').screenshot({ path: 'screenshots/k-activity-failure.png' });
});

test('container editor: implicit chip, suggestion row → Add adds the depend; containers list start order', async ({ page }) => {
  await login(page, '/_opm/app/northwind/config/containers');
  const order = page.getByTestId('start-order');
  await expect(order).toBeVisible();
  const seq = (await order.locator('.seq').textContent())!.split(' → ');
  expect(seq.indexOf('mqtt')).toBeLessThan(seq.indexOf('northwind-backend-admin'));
  expect(seq.indexOf('nodered')).toBeLessThan(seq.indexOf('northwind-backend-admin'));
  await expect(page.getByTestId('fix-depend-northwind-backend-admin-mqtt')).toBeVisible();
  await expect(page.getByTestId('container-row-northwind-backend-admin')).toContainText('nodered');

  await page.getByTestId('container-row-northwind-backend-admin').click();
  await page.waitForURL(/containers\/northwind-backend-admin$/);
  const dep = page.getByTestId('depends');
  const imp = dep.getByTestId('implicit-dep-nodered');
  await expect(imp).toContainText('implicit (from expression)');
  await expect(imp).toHaveAttribute('title', /env NODERED_URL/);
  const sg = dep.getByTestId('dep-suggest-mqtt');
  await expect(sg).toContainText('northwind-backend-admin mentions mqtt');
  await expect(sg).toContainText('env MQTT_URL');
  await expect(sg).toContainText('add it to Depends on?');
  await dep.scrollIntoViewIfNeeded();
  await dep.screenshot({ path: 'screenshots/k-depends-suggest.png' });
  await dep.getByTestId('dep-suggest-add-mqtt').click();
  await expect(sg).toBeHidden();
  await expect(page.getByTestId('container-depends')).toContainText('mqtt');
  await expect(page.getByTestId('local-edit-bar')).toContainText('1 change (not committed)');
});

test('review drawer: V_DEPENDS_SUGGESTED info with Add; Add refreshes the review', async ({ page }) => {
  await login(page, '/_opm/app/northwind/config/containers/northwind-backend-terminal');
  // any local change so the review can run
  await page.getByTestId('container-restart').getByRole('radio', { name: 'on-failure' }).click();
  await page.getByTestId('review-changes').click();
  const d = page.getByTestId('review-drawer');
  await expect(d).toContainText('V_DEPENDS_SUGGESTED');
  await expect(d).toContainText('northwind-backend-admin mentions `mqtt`');
  await d.getByTestId('fix-depend-northwind-backend-admin-mqtt').click();
  await expect(d.getByTestId('fix-depend-northwind-backend-admin-mqtt')).toHaveCount(0);
  await expect(d).not.toContainText('mentions `mqtt`');
});
