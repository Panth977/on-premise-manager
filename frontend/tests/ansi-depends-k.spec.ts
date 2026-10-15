import { test, expect } from '@playwright/test';
import { readFileSync } from 'node:fs';
import { login, expectToast, SEED, commitAll, nav } from './helpers';

/** Round K (§21 #93): ANSI log tails in the Activities drawer + implicit / suggested depends in the editor and the publish review. */

test('activities: update.failed log tail renders ANSI (colors / raw / copy / download) and shows the hint', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  await login(page, 'admin', `/app/deployments/${SEED.deployment}/activities?type=update.failed`);
  await page.getByTestId('activity-row').filter({ hasText: 'northwind-backend-admin' }).first().click();
  const fail = page.getByTestId('activity-failure').filter({ hasText: 'northwind-backend-admin could not resolve' });
  await expect(fail.getByTestId('activity-failure-hint')).toContainText('northwind-backend-admin could not resolve mqtt (EAI_AGAIN)');
  await expect(fail.getByTestId('activity-failure-hint').locator('code')).toHaveText('mqtt');

  const log = fail.getByTestId('activity-logtail');
  const body = fail.getByTestId('activity-logtail-body');
  await expect(log).toHaveAttribute('data-colors', 'on');
  await expect(log).toHaveAttribute('data-esc-lost', 'false');
  // bold red "error"
  const err = body.locator('span', { hasText: /^error$/ });
  await expect(err).toHaveCount(1);
  const st = await err.evaluate((el) => ({ w: getComputedStyle(el).fontWeight, c: getComputedStyle(el).color }));
  expect(Number(st.w)).toBeGreaterThanOrEqual(600);
  expect(st.c).toBe('rgb(207, 34, 46)'); // --ansi-1 (light theme)
  // no escape garbage; "[1]" stays text; OSC title + erase line removed
  await expect(body).not.toContainText('[0m');
  await expect(body).not.toContainText('␛');
  await expect(body).not.toContainText(']0;');
  await expect(body).toContainText('[1] listening on http://0.0.0.0:3000');
  await expect(body).toContainText('error: Uncaught (in promise) Error: getaddrinfo EAI_AGAIN mqtt');
  await expect(body).toContainText('exited with code 1');
  // 256-colour dim "at"
  const at = body.locator('span', { hasText: /^at $/ }).first();
  expect(await at.evaluate((el) => getComputedStyle(el).color)).toBe('rgb(138, 138, 138)');

  await page.getByTestId('activity-drawer').screenshot({ path: 'screenshots/k-ansi-light.png' });
  await page.emulateMedia({ colorScheme: 'dark' });
  await page.getByTestId('activity-drawer').screenshot({ path: 'screenshots/k-ansi-dark.png' });
  await page.emulateMedia({ colorScheme: 'light' });

  // a stream that lost its ESC bytes: bare SGR read as colours, "[1]" untouched
  const lost = page.getByTestId('activity-failure').filter({ hasText: 'northwind-backend-worker' });
  await expect(lost.getByTestId('activity-logtail')).toHaveAttribute('data-esc-lost', 'true');
  const lostBody = lost.getByTestId('activity-logtail-body');
  await expect(lostBody).toContainText('[1] worker booting');
  await expect(lostBody).not.toContainText('[0m');
  expect(await lostBody.locator('span', { hasText: /^error$/ }).evaluate((el) => getComputedStyle(el).color)).toBe('rgb(207, 34, 46)');
  await expect(lost.getByTestId('activity-failure-hint')).toHaveCount(0);

  // Copy = the visible text
  await log.getByTestId('activity-logtail-copy').click();
  const clip = await page.evaluate(() => navigator.clipboard.readText());
  expect(clip).toContain('error: Uncaught (in promise) Error: getaddrinfo EAI_AGAIN mqtt');
  expect(clip).not.toContain('\x1b');
  expect(clip).not.toContain('[0m');

  // Download = the raw bytes
  const [dl] = await Promise.all([page.waitForEvent('download'), log.getByTestId('activity-logtail-download').click()]);
  const raw = readFileSync((await dl.path())!, 'utf8');
  expect(raw).toContain('\x1b[0m\x1b[1m\x1b[31merror\x1b[0m: Uncaught');

  // Colors off → plain text, no styled spans
  await log.getByTestId('activity-logtail-colors').click();
  await expect(log).toHaveAttribute('data-colors', 'off');
  await expect(body.locator('span[style]')).toHaveCount(0);
  await expect(body).toContainText('error: Uncaught (in promise)');
  await expect(body).not.toContainText('[0m');

  // Raw → ESC shown as ␛
  await log.getByTestId('activity-logtail-raw').click();
  await expect(log).toHaveAttribute('data-raw', 'on');
  await expect(body).toContainText('␛[0m␛[1m␛[31merror␛[0m: Uncaught');
  await expect(body.locator('span[style]')).toHaveCount(0);

  // the older failure (plain text) still renders
  await page.keyboard.press('Escape');
  await page.getByTestId('activity-row').filter({ hasText: 'hello-api' }).first().click();
  await expect(page.getByTestId('activity-failure')).toContainText('fatal: DB_URL is not set');
  await expect(page.getByTestId('activity-failure-hint')).toHaveCount(0);
});

test('container editor: suggested depend row + Add, issue Add, implicit chip, start order', async ({ page }) => {
  const S = `/app/deployments/${SEED.deploymentEmpty}/sites/main`;
  await login(page, 'admin', `${S}/containers`);
  await expect(page.getByTestId('start-order')).toContainText('mqtt → northwind-backend-admin → northwind-backend-worker');

  await nav(page, `${S}/containers/northwind-backend-admin`);
  const row = page.getByTestId('dep-suggest-mqtt');
  await expect(row).toContainText('northwind-backend-admin mentions mqtt — add it to Depends on?');
  await expect(row).toContainText('env MQTT_URL');
  const issues = page.getByTestId('container-issues');
  await expect(issues.locator('li[data-code="V_DEPENDS_SUGGESTED"]')).toHaveCount(1);
  await expect(issues.getByTestId('issue-add-depend-mqtt')).toBeVisible();
  await expect(page.getByTestId('container-depends')).not.toContainText('mqtt');

  await page.locator('#sec-runtime').screenshot({ path: 'screenshots/k-depends-editor.png' });
  await row.getByTestId('dep-suggest-add-mqtt').click();
  await expect(page.getByTestId('dep-suggest-mqtt')).toHaveCount(0);
  await expect(issues.locator('li[data-code="V_DEPENDS_SUGGESTED"]')).toHaveCount(0);
  await expect(page.getByTestId('container-depends')).toContainText('mqtt');
  await page.getByTestId('apply-container').click();
  await expectToast(page, 'success', 'Container northwind-backend-admin applied');

  // implicit: the worker's ADMIN_URL uses containers.northwind-backend-admin.*
  await nav(page, `${S}/containers/northwind-backend-worker`);
  const chip = page.getByTestId('implicit-dep-northwind-backend-admin');
  await expect(chip).toContainText('northwind-backend-admin');
  await expect(chip).toContainText('implicit (from expression)');
  await expect(chip).toHaveAttribute('title', 'implicit (from expression): env ADMIN_URL');
  await expect(page.getByTestId('dep-suggest-northwind-backend-admin')).toHaveCount(0);
  await page.locator('#sec-runtime').screenshot({ path: 'screenshots/k-implicit-chip.png' });
});

test('publish review: suggestion row with Add writes the draft depend', async ({ page }) => {
  const D = `/app/deployments/${SEED.deploymentEmpty}`;
  await login(page, 'admin', `${D}/diff`);
  const sg = page.getByTestId('suggestions');
  await expect(sg).toContainText('northwind-backend-admin mentions mqtt (env MQTT_URL) — add it to Depends on?');
  await expect(sg.locator('li[data-code="V_DEPENDS_SUGGESTED"] .p code').first()).toHaveText('V_DEPENDS_SUGGESTED');
  await sg.screenshot({ path: 'screenshots/k-review-suggestions.png' });
  await sg.getByTestId('review-add-depend-northwind-backend-admin-mqtt').click();
  await expectToast(page, 'success', 'mqtt added to northwind-backend-admin');
  await expect(page.getByTestId('suggestions')).toHaveCount(0);
  await commitAll(page, 'mqtt before the admin');
  await nav(page, `${D}/sites/main/containers/northwind-backend-admin`);
  await expect(page.getByTestId('container-depends')).toContainText('mqtt');
  await expect(page.getByTestId('dep-suggest-mqtt')).toHaveCount(0);
});

