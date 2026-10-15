import { test, expect } from '@playwright/test';
import { login, SEED } from './helpers';

/**
 * SPEC-final §21 #92 — nothing is one-time: every ENROLLED site has an always-viewable "Upgrade / reinstall command"
 * (RevealValue, 60 s, logged), Regenerate = a site-bound maintenance token; agent clock skew warning; the console uses
 * the cloud's clock for relative times when the browser clock is wrong ("Last seen in 5 h").
 * Mock (DB_VERSION 19): ACME app = install token stored (View), db = 0.9.0 with a token from before commands were kept
 * (not stored → Regenerate) + clock 5 h 30 min ahead, lab = on the pin.
 */
const D = `/app/deployments/${SEED.deployment}`;
test.use({ permissions: ['clipboard-read', 'clipboard-write'], viewport: { width: 1440, height: 1000 } });

test('enrolled site: the upgrade command is viewable (60 s ring, Copy), options add OPM_YES / OPM_AGENT_VERSION, the view is logged', async ({ page }) => {
  await login(page, 'admin', `${D}/setup`);
  const app = page.getByTestId('setup-site-app');
  const uc = app.getByTestId('upgrade-command');
  await expect(uc).toBeVisible();
  await expect(uc).toHaveAttribute('data-kind', 'install');
  await expect(uc).toHaveAttribute('data-stored', '1');
  await expect(uc.getByTestId('upgrade-command-meta')).toContainText('the install command that enrolled this site');
  await expect(uc.getByTestId('upgrade-cmd-masked')).toBeVisible();
  await uc.getByTestId('upgrade-cmd-view').click();
  const v = uc.getByTestId('upgrade-cmd-value');
  await expect(v).toHaveText(/^curl -fsSL -H "Authorization: Bearer opi_[A-Za-z0-9_-]+" https:\/\/app\.opm\.example\/v1\/install\/script \| sudo sh$/);
  await expect(uc.getByTestId('upgrade-cmd-countdown')).toBeVisible();
  await uc.getByTestId('upgrade-cmd-copy').click();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(await v.textContent());
  // options: unattended + version pin (+ downgrade) — shown and copied
  await uc.getByTestId('upgrade-options').locator('summary').click();
  await uc.getByTestId('upg-opt-yes').click();
  await expect(v).toContainText('| sudo OPM_YES=1 sh');
  await uc.getByTestId('upg-opt-version').fill('0.9.0-10080530');
  await expect(v).toContainText('| sudo OPM_YES=1 OPM_AGENT_VERSION=0.9.0-10080530 sh');
  await uc.getByTestId('upg-opt-downgrade').click();
  await expect(v).toContainText('| sudo OPM_YES=1 OPM_AGENT_VERSION=0.9.0-10080530 OPM_ALLOW_DOWNGRADE=1 sh');
  await uc.getByTestId('upgrade-cmd-copy').click();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toContain('sudo OPM_YES=1 OPM_AGENT_VERSION=0.9.0-10080530 OPM_ALLOW_DOWNGRADE=1 sh');
  await uc.getByTestId('upg-opt-version').fill('bad version!');
  await expect(v).toContainText('| sudo OPM_YES=1 sh'); // an invalid version is never put into the command
  await page.screenshot({ path: 'screenshots/upgrade-cmd-view.png', fullPage: false });
  // the token row is marked as the site's upgrade command
  await expect(app.getByTestId('token-upgrade')).toHaveText('upgrade command');
  // logged
  await page.goto(`${D}/activities`);
  await expect(page.getByTestId('activity-row').filter({ hasText: 'install-command.revealed' }).first()).toBeVisible();
});

test('old token (not stored) inside the rerun-install hint → Regenerate → maintenance command viewable; old one revoked', async ({ page }) => {
  await login(page, 'admin', `${D}/setup`);
  const db = page.getByTestId('setup-site-db');
  // agent 0.9.0 < 0.10.0: the upgrade path holds the command inline
  const path = db.getByTestId('upgrade-path');
  await expect(path).toBeVisible();
  const uc = path.getByTestId('upgrade-command');
  await expect(uc).toHaveAttribute('data-stored', '0');
  await expect(uc.getByTestId('upgrade-cmd-not-stored')).toContainText('not stored — Regenerate');
  await expect(uc.getByTestId('upgrade-command-history')).toBeVisible();
  await expect(db.getByTestId('upgrade-command')).toHaveCount(1); // only once per site
  await page.screenshot({ path: 'screenshots/upgrade-cmd-not-stored.png' });
  await uc.getByTestId('upgrade-cmd-regenerate').click();
  const dlg = page.getByTestId('upgrade-cmd-confirm');
  await expect(dlg).toContainText('never enrolls a new one');
  await dlg.getByTestId('confirm-ok').click();
  const uc2 = page.getByTestId('setup-site-db').getByTestId('upgrade-command');
  await expect(uc2).toHaveAttribute('data-kind', 'maintenance');
  await expect(uc2).toHaveAttribute('data-stored', '1');
  await expect(uc2.getByTestId('upgrade-command-meta')).toContainText('maintenance command · regenerated');
  await expect(uc2.getByTestId('upgrade-cmd-value')).toHaveText(/^curl -fsSL -H "Authorization: Bearer opi_/); // shown at once after Regenerate
  const first = await uc2.getByTestId('upgrade-cmd-value').textContent();
  // the table: the maintenance token (upgrade only, no expiry) is the upgrade command; the old install token is revoked
  const rows = page.getByTestId('setup-site-db').getByTestId('token-row');
  await expect(rows.filter({ has: page.getByTestId('token-maintenance') })).toContainText('no expiry');
  await expect(rows.filter({ has: page.getByTestId('token-maintenance') }).getByTestId('token-upgrade')).toBeVisible();
  await expect(rows.filter({ hasText: 'revoked' })).toHaveCount(1);
  // View again later = the same command
  await page.reload();
  const uc3 = page.getByTestId('setup-site-db').getByTestId('upgrade-command');
  await uc3.getByTestId('upgrade-cmd-view').click();
  await expect(uc3.getByTestId('upgrade-cmd-value')).toHaveText(first!);
  // Regenerate again → a different command
  await uc3.getByTestId('upgrade-cmd-regenerate').click();
  await page.getByTestId('upgrade-cmd-confirm').getByTestId('confirm-ok').click();
  await expect(page.getByTestId('setup-site-db').getByTestId('upgrade-cmd-value')).not.toHaveText(first!);
  await page.goto(`${D}/activities`);
  await expect(page.getByTestId('activity-row').filter({ hasText: 'install-command.regenerated' })).toHaveCount(2);
});

test('agent clock skew: warning when > 2 min (db 5 h 30 min ahead), quiet "in sync" otherwise; Status page too', async ({ page }) => {
  await login(page, 'admin', `${D}/setup`);
  const w = page.getByTestId('setup-site-db').getByTestId('clock-skew-warning');
  await expect(w).toHaveText('agent clock is 5 h 30 min ahead — fix NTP on the server (e.g. sudo timedatectl set-ntp true)');
  await expect(page.getByTestId('setup-site-app').getByTestId('clock-skew-ok')).toContainText('in sync');
  await expect(page.getByTestId('setup-site-app').getByTestId('clock-skew-warning')).toHaveCount(0);
  await expect(page.getByTestId('setup-site-lab').getByTestId('clock-skew-unknown')).toBeVisible();
  await w.scrollIntoViewIfNeeded();
  await page.screenshot({ path: 'screenshots/upgrade-cmd-skew.png' });
  await page.goto(`${D}/status`);
  await expect(page.getByTestId('status-row-db').getByTestId('clock-skew-warning')).toContainText('5 h 30 min ahead');
});

test('a wrong BROWSER clock (5 h 30 min behind) never shows "Last seen in 5 h": times use the cloud clock + a note', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('opm.mock.serverSkewMs', String(5.5 * 3600_000)));
  await login(page, 'admin', `${D}/setup`);
  const seen = page.getByTestId('setup-site-app').getByTestId('last-seen');
  await expect(seen.locator('time')).toHaveText(/^(just now|\d+ s ago)$/);
  const note = page.getByTestId('browser-clock-note');
  await expect(note).toContainText('5 h 30 min behind');
  await page.screenshot({ path: 'screenshots/upgrade-cmd-browser-clock.png' });
  await page.goto(`${D}/status`);
  await expect(page.getByTestId('status-row-app').locator('time').first()).toContainText(/just now|ago/);
  await expect(page.getByTestId('browser-clock-note')).toBeVisible();
});

test('a correct browser clock: no note', async ({ page }) => {
  await login(page, 'admin', `${D}/setup`);
  await expect(page.getByTestId('setup-site-app').getByTestId('upgrade-command')).toBeVisible();
  await expect(page.getByTestId('browser-clock-note')).toHaveCount(0);
});
