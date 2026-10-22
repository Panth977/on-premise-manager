/** Round I (SPEC §21 #90): the Agent page — everything opm-agent does on the command line, incl. the upgrade. Mock agent. */
import type { Page } from '@playwright/test';
import { test, expect } from './fixtures';
import { knobs, login, open, shot, EMAIL, nav } from './helpers';

const PIN = '0.10.1-10081500';
const OLD = '0.10.0-10080900';

async function agentPage(page: Page, site = 'app') {
  await login(page, `/_opm/app/${site}/agent`);
  await expect(page.getByTestId('page-title')).toHaveText(`Agent · ${site}`);
  await expect(page.getByTestId('agent-cards')).toBeVisible();
}

test('info cards: version / status parity + sidebar item + upgrade badge', async ({ page }) => {
  await knobs(page, { 'opm.mock.restartSec': '2' });
  await agentPage(page);
  await expect(page.getByTestId('tree-app-agent')).toHaveAttribute('aria-current', 'page');
  await expect(page.getByTestId('tree-app-agent').getByTestId('nav-agent-upgrade')).toHaveText('up'); // app below the pin
  await page.getByTestId('tree-toggle-db').click();
  await page.getByTestId('tree-toggle-db-server').click();
  await expect(page.getByTestId('tree-db-agent').getByTestId('nav-agent-upgrade')).toHaveText('up'); // db too
  await expect(page.getByTestId('agent-version')).toHaveText(OLD);
  await expect(page.getByTestId('upgrade-available')).toContainText(PIN);
  const v = page.getByTestId('card-version');
  await expect(v).toContainText('go1.23.4');
  await expect(v).toContainText('f39184b5c0e1');
  await expect(page.getByTestId('prev-binary')).toContainText('0.9.0');
  await expect(page.getByTestId('card-process')).toContainText('opm-agent.service');
  await expect(page.getByTestId('boot-guard')).toContainText('installed');
  await expect(page.getByTestId('card-paths')).toContainText('/etc/opm/agent.json');
  await expect(page.getByTestId('card-paths')).toContainText('/usr/local/bin/opm-agent.prev');
  await expect(page.getByTestId('card-ports')).toContainText('7080');
  await expect(page.getByTestId('card-ports')).toContainText('8090');
  await expect(page.getByTestId('card-docker')).toContainText('28.5.2');
  await expect(page.getByTestId('card-docker')).toContainText('1.47');
  await expect(page.getByTestId('enroll-state')).toHaveText('enrolled');
  await expect(page.getByTestId('card-enrollment')).toContainText('SHA256:');
  await expect(page.getByTestId('console-link')).toHaveAttribute('href', /opm\.example\.com\/app\/deployments\//);
  await expect(page.getByTestId('rel-badge')).toHaveText('Upgrade available');
  await page.getByTestId('release-notes').locator('summary').click();
  await expect(page.getByTestId('release-notes')).toContainText('agent self-management in the GUI');
  await expect(page.getByTestId('upgrade-history')).toContainText('0.9.0 → ' + OLD);
  await shot(page, 'i-agent-light');
  // status page + sites tiles link here
  await open(page, '/_opm/app/app/status');
  await expect(page.getByTestId('agent-hint-app')).toContainText('upgrade available');
  await page.getByTestId('agent-hint-app').click();
  await expect(page).toHaveURL(/\/_opm\/app\/app\/agent$/);
  await open(page, '/_opm/app');
  await expect(page.getByTestId('tile-upgrade-db')).toBeVisible();
});

test('revoked site: re-enroll help lines are copyable', async ({ page }) => {
  await knobs(page, { 'opm.mock.agentRevoked': '1' });
  await agentPage(page);
  await expect(page.getByTestId('enroll-state')).toHaveText('revoked');
  await expect(page.getByTestId('reenroll-help')).toContainText('sudo mv /var/lib/opm /var/lib/opm.revoked-');
  await page.getByTestId('reenroll-copy').click();
  await expect(page.getByText('Copied re-enroll steps')).toBeVisible();
});

test('check release → verification card (verify-release parity)', async ({ page }) => {
  await agentPage(page);
  await page.getByTestId('check-release').click();
  const v = page.getByTestId('verification');
  await expect(v).toContainText(`Release ${PIN} verified`);
  await expect(page.getByTestId('ver-signature')).toContainText('valid');
  await expect(v).toContainText('agent.json releasePublicKey');
  await expect(v).toContainText('SHA256:3q8xJm0F');
  await expect(page.getByTestId('ver-sha')).toContainText('checked after the download');
  await shot(page, 'i-check-release-light', false);
});

test('check release with a bad signature', async ({ page }) => {
  await knobs(page, { 'opm.mock.agentFail': 'verify' });
  await agentPage(page);
  await page.getByTestId('check-release').click();
  await expect(page.getByTestId('verification')).toContainText('NOT verified');
  await expect(page.getByTestId('ver-signature')).toContainText('invalid');
});

test('upgrade happy path: stepper, restart overlay reconnects on the SAME session, "Now running"', async ({ page }) => {
  test.setTimeout(90_000);
  await knobs(page, { 'opm.mock.restartSec': '4' });
  await agentPage(page);
  await page.getByTestId('upgrade-open').click();
  const dlg = page.getByTestId('upgrade-dialog');
  await expect(dlg).toBeVisible();
  await expect(page.getByTestId('upgrade-from-to')).toHaveText(`${OLD} → ${PIN}`);
  await expect(dlg).toContainText(EMAIL);
  await expect(dlg).toContainText('opm-agent.prev');
  await expect(dlg).toContainText('within 60 s');
  await expect(page.getByTestId('upgrade-go')).toBeDisabled();
  await page.getByTestId('upgrade-confirm').fill('db');
  await expect(page.getByTestId('upgrade-go')).toBeDisabled();
  await page.getByTestId('upgrade-confirm').fill('app');
  await shot(page, 'i-upgrade-dialog-light', false);
  await page.getByTestId('upgrade-go').click();
  const prog = page.getByTestId('upgrade-progress');
  await expect(prog).toBeVisible();
  await expect(page.getByTestId('step-download')).toHaveAttribute('data-state', 'active');
  await expect(page.getByTestId('download-pct')).toContainText('%');
  await shot(page, 'i-upgrade-download-light', false);
  await expect(page.getByTestId('step-verify')).toHaveAttribute('data-state', /active|done/, { timeout: 8000 });
  // restart: the blocking overlay polls /info, the agent is down for 4 s
  const ov = page.getByTestId('restart-overlay');
  await expect(ov).toBeVisible({ timeout: 10_000 });
  await expect(page.getByTestId('restart-status')).toContainText('stopped', { timeout: 8000 });
  await shot(page, 'i-restart-overlay-light', false);
  await expect(ov).toBeHidden({ timeout: 30_000 });
  await expect(page.getByTestId('upgrade-done')).toContainText(`Now running ${PIN}`, { timeout: 15_000 });
  await expect(page.getByText(`Now running ${PIN}`).first()).toBeVisible();
  // same session: no login page, no actor popup, the actor chip is still there
  await expect(page).toHaveURL(/\/_opm\/app\/app\/agent$/);
  await expect(page.getByTestId('actor-popup')).toBeHidden();
  await expect(page.getByTestId('actor-chip')).toContainText(EMAIL);
  await expect(page.getByTestId('agent-version')).toHaveText(PIN);
  await expect(page.getByTestId('rel-badge')).toHaveText('Up to date');
  await expect(page.getByTestId('prev-binary')).toContainText(OLD);
  await expect(page.getByTestId('upgrade-history').getByTestId('hist-done').first()).toContainText(`${OLD} → ${PIN}`);
  await shot(page, 'i-upgrade-done-light');
  // activities show the new types with labels
  await nav(page, 'activities'); // client-side: the mock keeps its state
  await expect(page).toHaveURL(/\/_opm\/app\/app\/activities$/);
  await expect(page.getByTestId('act-type-agent.upgraded').first()).toContainText('Agent upgraded');
  await expect(page.getByTestId('act-type-agent.upgrade-started').first()).toContainText('Upgrade started');
  await page.getByTestId('act-type-agent.upgraded').first().click();
  await expect(page.getByTestId('activity-facts')).toContainText(`${OLD} → ${PIN}`);
  await shot(page, 'i-activities-agent-light', false);
});

test('session gone after the restart → login with a clear message', async ({ page }) => {
  await knobs(page, { 'opm.mock.restartSec': '2', 'opm.mock.restartDropsSession': '1' });
  await agentPage(page);
  await page.getByTestId('restart-open').click();
  await page.getByTestId('restart-confirm').fill('app');
  await page.getByTestId('restart-go').click();
  await expect(page.getByTestId('restart-overlay')).toBeVisible();
  await page.waitForURL(/\/_opm\/login/, { timeout: 30_000 });
  await expect(page.getByText('The agent restarted and this login session is no longer valid')).toBeVisible();
});

test('rollback: the new agent is not healthy → failure banner with the reason + history', async ({ page }) => {
  test.setTimeout(90_000);
  await knobs(page, { 'opm.mock.restartSec': '2', 'opm.mock.agentFail': 'health' });
  await agentPage(page);
  await page.getByTestId('upgrade-open').click();
  await page.getByTestId('upgrade-confirm').fill('app');
  await page.getByTestId('upgrade-go').click();
  await expect(page.getByTestId('upgrade-rolled-back')).toBeVisible({ timeout: 45_000 });
  await expect(page.getByTestId('upgrade-rolled-back')).toContainText(`Rolled back to ${OLD}`);
  await expect(page.getByTestId('upgrade-rolled-back')).toContainText('did not report healthy within 60 s');
  await expect(page.getByTestId('step-health')).toHaveAttribute('data-state', 'failed');
  await expect(page.getByTestId('agent-version')).toHaveText(OLD);
  await expect(page.getByTestId('upgrade-history').getByTestId('hist-rolled-back')).toBeVisible();
  await expect(page.getByTestId('actor-chip')).toContainText(EMAIL);
  await shot(page, 'i-rollback-light');
});

test('verify failure: nothing changes, no restart', async ({ page }) => {
  await knobs(page, { 'opm.mock.agentFail': 'verify' });
  await agentPage(page);
  await page.getByTestId('upgrade-open').click();
  await page.getByTestId('upgrade-confirm').fill('app');
  await page.getByTestId('upgrade-go').click();
  await expect(page.getByTestId('upgrade-failed')).toContainText('failed in verify', { timeout: 15_000 });
  await expect(page.getByTestId('upgrade-failed')).toContainText(`keeps running ${OLD}`);
  await expect(page.getByTestId('step-verify')).toHaveAttribute('data-state', 'failed');
  await expect(page.getByTestId('restart-overlay')).toBeHidden();
});

test('§21 #110 busy (snapshot running) → a warning only: upgrade / restart / save stay enabled', async ({ page }) => {
  await knobs(page, { 'opm.mock.agentBusy': 'snapshot' });
  await agentPage(page);
  await expect(page.getByTestId('agent-busy-note')).toContainText('A snapshot is running on app');
  await expect(page.getByTestId('agent-busy')).toHaveCount(0);
  await expect(page.getByTestId('upgrade-open')).toBeEnabled();
  await expect(page.getByTestId('upgrade-blocked')).toHaveCount(0);
  await expect(page.getByTestId('restart-open')).toBeEnabled();
  await page.getByTestId('set-hostShell').uncheck();
  await page.getByTestId('config-confirm').fill('app');
  await expect(page.getByTestId('config-save-restart')).toBeEnabled();
  await shot(page, 'i-busy-light', false);
});

test('409 stale (the pin moved) and conflict are explained with a reload', async ({ page }) => {
  await agentPage(page);
  // the pin moves after the page loaded: the mock answers with the installed version as the pin
  await page.getByTestId('upgrade-open').click();
  await page.evaluate(() => localStorage.setItem('opm.mock.agentPinSame', '1'));
  await page.getByTestId('upgrade-confirm').fill('app');
  await page.getByTestId('upgrade-go').click();
  await expect(page.getByTestId('upgrade-error')).toContainText('The pinned release moved');
  await page.evaluate(() => localStorage.removeItem('opm.mock.agentPinSame'));
  await page.getByTestId('upgrade-reload').click();
  await expect(page.getByTestId('upgrade-dialog')).toBeHidden();
});

test('upgrade all: sequential (db, then app last), northwind skipped, then stop on the first failure', async ({ page }) => {
  test.setTimeout(120_000);
  await knobs(page, { 'opm.mock.restartSec': '2' });
  await login(page, '/_opm/app/*/agent');
  await expect(page.getByTestId('agent-row-northwind')).toContainText('older than 0.10.0');
  const plan = page.getByTestId('upgrade-all-plan');
  await expect(plan.getByTestId('plan-db')).toHaveAttribute('data-kind', 'upgrade');
  await expect(plan.getByTestId('plan-northwind')).toHaveAttribute('data-kind', 'old');
  await expect(plan.getByTestId('plan-northwind')).toContainText('rerun the install command');
  await expect(plan.getByTestId('plan-app')).toContainText('upgraded last');
  // order: db, northwind, app (this site last)
  const rows = plan.locator('tbody tr');
  await expect(rows.nth(0)).toHaveAttribute('data-testid', 'plan-db');
  await expect(rows.nth(2)).toHaveAttribute('data-testid', 'plan-app');
  await shot(page, 'i-upgrade-all-plan-light');
  await page.getByTestId('upgrade-all-open').click();
  await expect(page.getByTestId('confirm-ok')).toBeDisabled();
  await page.getByTestId('upgrade-all-confirm').locator('input').fill('UPGRADE');
  await page.getByTestId('confirm-ok').click();
  const runp = page.getByTestId('upgrade-all-run');
  await expect(runp.getByTestId('run-site-db')).toHaveAttribute('data-status', 'running');
  await expect(runp.getByTestId('run-site-app')).toHaveAttribute('data-status', 'pending');
  await expect(runp.getByTestId('run-site-northwind')).toHaveAttribute('data-status', 'skipped');
  await shot(page, 'i-upgrade-all-run-light', false);
  await expect(runp.getByTestId('run-site-db')).toHaveAttribute('data-status', 'done', { timeout: 30_000 });
  // the coordinator restarts itself last: overlay, then the run finishes on the same session
  await expect(page.getByTestId('restart-overlay')).toBeVisible({ timeout: 20_000 });
  await expect(page.getByTestId('upgrade-all-status')).toHaveText('done', { timeout: 40_000 });
  await expect(runp.getByTestId('run-site-app')).toHaveAttribute('data-status', 'done');
  await expect(page.getByTestId('actor-chip')).toContainText(EMAIL);
  await expect(page.getByTestId('agent-row-app')).toContainText(PIN);
  await shot(page, 'i-upgrade-all-done-light', false);
});

test('upgrade all stops at the first failure (db rolls back → app not started) + cancel', async ({ page }) => {
  test.setTimeout(90_000);
  await knobs(page, { 'opm.mock.restartSec': '1', 'opm.mock.agentFail': 'health:db' });
  await login(page, '/_opm/app/*/agent');
  await page.getByTestId('upgrade-all-open').click();
  await page.getByTestId('upgrade-all-confirm').locator('input').fill('UPGRADE');
  await page.getByTestId('confirm-ok').click();
  const runp = page.getByTestId('upgrade-all-run');
  await expect(page.getByTestId('upgrade-all-status')).toHaveText('failed', { timeout: 45_000 });
  await expect(runp.getByTestId('run-site-db')).toHaveAttribute('data-status', 'failed');
  await expect(runp.getByTestId('run-site-db')).toContainText('did not report healthy');
  await expect(runp.getByTestId('run-site-app')).toHaveAttribute('data-status', 'cancelled');
  await expect(page.getByTestId('restart-overlay')).toBeHidden();
  await expect(page.getByTestId('agent-row-app')).toContainText(OLD);
  await shot(page, 'i-upgrade-all-failed-light', false);
  // a new run can be cancelled: it stops after the current site
  await page.getByRole('button', { name: 'Show the plan again' }).click();
  await page.getByTestId('upgrade-all-open').click();
  await page.getByTestId('upgrade-all-confirm').locator('input').fill('UPGRADE');
  await page.getByTestId('confirm-ok').click();
  await expect(runp.getByTestId('run-site-db')).toHaveAttribute('data-status', 'running');
  await page.getByTestId('upgrade-all-cancel').click();
  await expect(page.getByTestId('upgrade-all-status')).toHaveText('cancelled', { timeout: 30_000 });
  await expect(runp.getByTestId('run-site-db')).toHaveAttribute('data-status', 'done');
  await expect(runp.getByTestId('run-site-app')).toHaveAttribute('data-status', 'cancelled');
});

test('restart agent: typed slug + reason → overlay → back on the same session', async ({ page }) => {
  await knobs(page, { 'opm.mock.restartSec': '3' });
  await agentPage(page);
  const pid = await page.getByTestId('card-process').locator('dd').first().textContent();
  await page.getByTestId('restart-open').click();
  await page.getByTestId('restart-reason').fill('test restart');
  await expect(page.getByTestId('restart-go')).toBeDisabled();
  await page.getByTestId('restart-confirm').fill('app');
  await shot(page, 'i-restart-dialog-light', false);
  await page.getByTestId('restart-go').click();
  await expect(page.getByTestId('restart-overlay')).toBeVisible();
  await expect(page.getByTestId('restart-overlay')).toBeHidden({ timeout: 30_000 });
  await expect(page.getByText('The agent is back')).toBeVisible();
  await expect(page.getByTestId('card-process').locator('dd').first()).not.toHaveText(pid ?? '');
  await expect(page.getByTestId('actor-chip')).toContainText(EMAIL);
  await expect(page).toHaveURL(/\/_opm\/app\/app\/agent$/);
});

test('restart another site (db): no overlay, the page waits for it', async ({ page }) => {
  await knobs(page, { 'opm.mock.restartSec': '2' });
  await agentPage(page, 'db');
  await page.getByTestId('restart-open').click();
  await page.getByTestId('restart-confirm').fill('db');
  await page.getByTestId('restart-go').click();
  await expect(page.getByText('db is back')).toBeVisible({ timeout: 20_000 });
  await expect(page.getByTestId('restart-overlay')).toBeHidden();
});

test('agent.json: masked view, live validation, diff, save and restart; save only → restart pending', async ({ page }) => {
  await knobs(page, { 'opm.mock.restartSec': '2' });
  await agentPage(page);
  const cfg = page.getByTestId('agent-config');
  await page.getByTestId('config-raw-toggle').click();
  await expect(page.getByTestId('config-masked')).toContainText('"installToken": "••••');
  await expect(page.getByTestId('config-masked')).toContainText('releasePublicKey');
  await page.getByTestId('config-copy').click();
  await expect(page.getByText('Copied agent.json (masked)')).toBeVisible();
  // validation: the agent port is refused, an equal alias/edge port is refused, "none" edge warns
  await page.getByTestId('set-guiAliasPort').fill('7080');
  await expect(page.getByTestId('issue-guiAliasPort-error')).toContainText('agent port');
  await page.getByTestId('set-guiAliasPort').fill('70000');
  await expect(page.getByTestId('issue-guiAliasPort-error')).toContainText('1–65535');
  await page.getByTestId('set-guiAliasPort').fill('8081');
  await expect(page.getByTestId('issue-guiAliasPort-error')).toContainText('default edge port');
  await page.getByTestId('config-confirm').fill('app');
  await expect(page.getByTestId('config-save-restart')).toBeDisabled();
  await page.getByTestId('set-guiAliasPort').fill('8095');
  await page.getByTestId('set-edgeHttpPort-none').check();
  await expect(page.getByTestId('issue-edgeHttpPort-warning')).toContainText('no default edge');
  await page.getByTestId('set-edgeHttpPort-none').uncheck();
  await page.getByTestId('set-containerNames').getByRole('radio', { name: 'prefixed' }).click();
  await expect(page.getByTestId('issue-containerNames-info')).toContainText('renamed in place');
  await page.getByTestId('set-hostShell').uncheck();
  const diff = page.getByTestId('config-diff');
  await expect(diff).toContainText('guiAliasPort');
  await expect(diff).toContainText('8095');
  await expect(diff).toContainText('containerNames');
  await expect(diff).toContainText('tools.hostShell');
  await shot(page, 'i-config-editor-light', false);
  await expect(page.getByTestId('config-save-restart')).toBeEnabled();
  await page.getByTestId('config-save-restart').click();
  await expect(page.getByTestId('restart-overlay')).toBeVisible();
  await expect(page.getByTestId('restart-overlay')).toBeHidden({ timeout: 30_000 });
  await expect(page.getByTestId('card-ports')).toContainText('8095');
  await expect(cfg.getByTestId('restart-pending')).toBeHidden();
  // save only → restart pending badge
  await page.getByTestId('set-containerExec').uncheck();
  await page.getByTestId('config-confirm').fill('app');
  await page.getByTestId('config-save-only').click();
  await expect(page.getByText('applies at the next restart')).toBeVisible();
  await expect(cfg.getByTestId('restart-pending')).toBeVisible();
  await expect(page.getByTestId('restart-overlay')).toBeHidden();
});

test('agent.json read-only: the editor is disabled with the reason', async ({ page }) => {
  await knobs(page, { 'opm.mock.agentReadOnly': '1' });
  await agentPage(page);
  await expect(page.getByTestId('config-readonly')).toContainText('owned by root');
  await expect(page.getByTestId('set-hostShell')).toBeDisabled();
  await expect(page.getByTestId('set-guiAliasPort')).toBeDisabled();
});

test('support bundle downloads; uninstall commands copy; CLI map; Backups parity link', async ({ page }) => {
  await agentPage(page);
  await expect(page.getByTestId('bundle-sections')).toContainText('activities.jsonl');
  await expect(page.getByTestId('bundle-sections')).toContainText('debug/');
  const dl = page.waitForEvent('download');
  await page.getByTestId('support-bundle').click();
  const d = await dl;
  expect(d.suggestedFilename()).toMatch(/^opm-support-app-\d{8}-\d{6}\.tar\.gz$/);
  await expect(page.getByTestId('uninstall-lines')).toContainText('sudo systemctl disable --now opm-agent');
  await expect(page.getByTestId('uninstall-lines')).toContainText('/usr/local/bin/opm-agent.prev');
  await page.getByTestId('uninstall-copy').click();
  await expect(page.getByText('Copied uninstall commands')).toBeVisible();
  const cli = page.getByTestId('cli-map');
  for (const c of ['opm-agent version', 'opm-agent status', 'opm-agent verify-release', 'opm-agent upgrade', 'opm-agent snapshot list', 'opm-agent snapshot export', 'opm-agent snapshot import', 'opm-agent keygen', 'opm-agent enroll', 'opm-agent install']) await expect(cli).toContainText(c);
  await expect(cli.getByTestId('cli-opm-agent-keygen')).toContainText('CLI only');
  await shot(page, 'i-agent-ops-light');
  // the support-bundle activity was written (client-side navigation: the mock keeps its state)
  await nav(page, 'activities');
  await expect(page.getByTestId('act-type-agent.support-bundle-downloaded').first()).toBeVisible();
  await nav(page, 'agent');
  await page.getByTestId('cli-map').getByTestId('cli-opm-agent-snapshot-export').getByRole('link').click();
  await expect(page).toHaveURL(/\/_opm\/app\/app\/backups$/);
  await expect(page.getByTestId('backups-cli')).toContainText('snapshot export');
});

test('Backups: list / export with and without images / import (snapshot list / export / import parity)', async ({ page }) => {
  await login(page, '/_opm/app/backups');
  const rows = page.getByTestId('snapshots').locator('tbody tr');
  await expect(rows).toHaveCount(3);
  const id = (await rows.first().locator('code').first().getAttribute('title'))!;
  for (const withImages of [false, true]) {
    await page.getByTestId(`download-${id}`).click();
    if (withImages) await page.getByTestId('dl-images').check();
    else await expect(page.getByTestId('dl-images')).not.toBeChecked();
    const dl = page.waitForEvent('download');
    await page.getByTestId('dl-go').click();
    const d = await dl;
    expect(d.suggestedFilename()).toMatch(/\.opmsnap$/);
    const body = (await (await import('node:fs')).promises.readFile((await d.path())!)).toString();
    expect(body.includes('(with images)')).toBe(withImages);
  }
  await page.getByTestId('import-input').setInputFiles({ name: 'x.opmsnap', mimeType: 'application/x-tar', buffer: Buffer.from('opmsnap/1 exported elsewhere') });
  await expect(rows).toHaveCount(4);
});

test('an older agent (northwind 0.9.0) explains the one-time install rerun; dark theme', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('opm.theme', 'dark'));
  await login(page, '/_opm/app/northwind/agent');
  await expect(page.getByTestId('empty-state')).toContainText('older than 0.10.0');
  await expect(page.getByTestId('empty-state')).toContainText('Rerun the install command of');
  await open(page, '/_opm/app/app/agent');
  await expect(page.getByTestId('agent-cards')).toBeVisible();
  await shot(page, 'i-agent-dark');
  await open(page, '/_opm/app/*/agent');
  await expect(page.getByTestId('agent-sites')).toBeVisible();
  await shot(page, 'i-agent-all-dark');
});

test('Sites tree: the Agent page of another site (no in-page picker)', async ({ page }) => {
  await agentPage(page);
  await expect(page.getByTestId('agent-site-select')).toHaveCount(0);
  await page.getByTestId('tree-toggle-db').click();
  await page.getByTestId('tree-toggle-db-server').click();
  await page.getByTestId('tree-db-agent').click();
  await expect(page).toHaveURL(/\/_opm\/app\/db\/agent$/);
  await expect(page.getByTestId('page-title')).toHaveText('Agent · db');
  await expect(page.getByTestId('card-docker')).toContainText('27.3.1');
});

test('§21 #91: health card (checks + socket activation), DB schema, hard fallback note; overlay uses /health', async ({ page }) => {
  await knobs(page, { 'opm.mock.restartSec': '2' });
  await agentPage(page);
  await expect(page.getByTestId('health-ok')).toHaveText('ok');
  await expect(page.getByTestId('health-checks')).toContainText('store');
  await expect(page.getByTestId('socket-activated')).toContainText('edge sockets held by systemd — no refused connections during restarts');
  await expect(page.getByTestId('db-schema')).toContainText('2');
  await expect(page.getByTestId('hard-fallback')).toContainText('rerun this site\'s original install command');
  await expect(page.getByTestId('hard-fallback')).toContainText('OPM_AGENT_VERSION=x');
  await shot(page, 'i-health-light', false);
  // upgrade: the overlay waits for /health ok with the new version; the schema follows the new build
  await page.getByTestId('upgrade-open').click();
  await page.getByTestId('upgrade-confirm').fill('app');
  await page.getByTestId('upgrade-go').click();
  await expect(page.getByTestId('restart-overlay')).toBeVisible({ timeout: 10_000 });
  await expect(page.getByTestId('restart-overlay')).toContainText('/_opm/api/health');
  await expect(page.getByTestId('upgrade-done')).toBeVisible({ timeout: 40_000 });
  await expect(page.getByTestId('db-schema')).toContainText('3');
  await expect(page.locator('nav.side')).toContainText(`agent ${PIN}`);
});

test('§21 #91: no socket unit → health card says ports close for a moment', async ({ page }) => {
  await knobs(page, { 'opm.mock.noSocket': '1' });
  await agentPage(page);
  await expect(page.getByTestId('socket-activated')).toContainText('no socket activation');
  // §21 #121 db's page shows db's health too (over the mesh)
  await open(page, '/_opm/app/db/agent');
  await expect(page.getByTestId('agent-cards')).toBeVisible();
  await expect(page.getByTestId('card-health')).toHaveCount(1);
});
