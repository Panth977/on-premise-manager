import { test, expect, type Page } from '@playwright/test';
import { login, SEED, expectToast } from './helpers';

/**
 * Round I (SPEC-final §21 #90): agent upgrades from the cloud console.
 * Mock seed (ACME): pinned release 0.10.0-10081200; app = 0.10.0-10080900 (upgrade available), db = 0.9.0-10080530
 * (below GUI_UPGRADE_MIN_VERSION → rerun-install hint), lab = 0.10.0-10081200 (up to date, enrolled, not in the draft).
 * Mock agents pick a request up 2 s after it was queued; the phases take ~7.3 s (download 3 s · verify · swap · restart · health).
 * Hook window.__opmMockAgent(dep, site, patch): { _upgHold: true } (never picks up), { _upgFail: 'download' | 'verify' | 'health' | 'pin' }.
 */
const E = `/app/deployments/${SEED.deployment}`;
const PIN = '0.10.0-10081200';
const SLOW = { timeout: 30_000 };

async function agent(page: Page, site: string, patch: Record<string, unknown>) {
  await page.waitForFunction(() => typeof (window as unknown as { __opmMockAgent?: unknown }).__opmMockAgent === 'function');
  const ok = await page.evaluate(([dep, s, p]) => (window as unknown as { __opmMockAgent: (d: string, s: string, p: unknown) => boolean }).__opmMockAgent(dep as string, s as string, p), [SEED.deployment, site, patch] as const);
  expect(ok).toBe(true);
}
async function requestUpgrade(page: Page, site: string) {
  const card = page.getByTestId(`setup-site-${site}`);
  await card.getByTestId('upgrade-agent').click();
  const dlg = page.getByTestId('upgrade-confirm');
  await expect(dlg).toBeVisible();
  await expect(dlg.getByTestId('confirm-ok')).toBeDisabled();
  await dlg.getByTestId('typed-confirm').fill(site);
  await dlg.getByTestId('confirm-ok').click();
  await expect(dlg).toHaveCount(0);
}

test.describe('Round I · agent upgrade (cloud console)', () => {
  test.use({ viewport: { width: 1440, height: 900 } });
  test('single upgrade: badge, typed confirm with from → to + notes, queued, progress + verification, done', async ({ page }) => {
    await login(page, 'admin', `${E}/setup`);
    const rel = page.getByTestId('agent-release-panel');
    await expect(rel.getByTestId('release-version')).toHaveText(PIN);
    await expect(rel.getByTestId('below-count')).toContainText('2 of 3 sites are below it');
    await rel.getByTestId('whats-new').locator('summary').click();
    await expect(rel.getByTestId('whats-new-body')).toContainText('Automatic rollback to the previous binary');

    const app = page.getByTestId('setup-site-app');
    await expect(app.getByTestId('agent-installed')).toHaveText('0.10.0-10080900');
    await expect(app.getByTestId('agent-latest')).toHaveText(PIN);
    await expect(app.getByTestId('upgrade-badge')).toHaveAttribute('data-state', 'available');
    await expect(page.getByTestId('setup-site-lab').getByTestId('upgrade-badge')).toHaveText('Up to date');

    await app.getByTestId('upgrade-agent').click();
    const dlg = page.getByTestId('upgrade-confirm');
    await expect(dlg.getByTestId('upgrade-fromto')).toContainText('0.10.0-10080900');
    await expect(dlg.getByTestId('upgrade-fromto')).toContainText(PIN);
    await dlg.getByTestId('upgrade-confirm-notes').locator('summary').click();
    await expect(dlg.getByTestId('upgrade-confirm-notes-body')).toContainText('signed download');
    await expect(dlg.getByTestId('confirm-ok')).toBeDisabled();
    await dlg.getByTestId('typed-confirm').fill('ap');
    await expect(dlg.getByTestId('confirm-ok')).toBeDisabled();
    await page.screenshot({ path: 'screenshots/i-confirm.png' });
    await dlg.getByTestId('typed-confirm').fill('app');
    await dlg.getByTestId('confirm-ok').click();
    await expectToast(page, 'success', 'Upgrade of app queued');

    await expect(app.getByTestId('upgrade-queued')).toContainText('Queued — the agent picks it up on its next poll (≤ ~30 s)');
    await expect(app.getByTestId('upgrade-badge')).toHaveAttribute('data-state', 'queued');
    await app.scrollIntoViewIfNeeded();
    await page.screenshot({ path: 'screenshots/i-queued.png' });

    // the agent starts: phase stepper (download % → verify → …)
    const prog = app.getByTestId('upgrade-progress');
    await expect(prog).toBeVisible(SLOW);
    await expect(prog.getByTestId('upgrade-step')).toHaveCount(6);
    await expect(prog.locator('[data-step="verify"]')).toHaveAttribute('data-state', /done|current/, SLOW);
    await expect(prog.getByTestId('upgrade-progress-verification')).toBeVisible();
    await expect(prog.getByTestId('verify-signature')).toContainText('valid');
    await expect(prog.getByTestId('verify-key-source')).toHaveText('agent.json releasePublicKey');
    await app.scrollIntoViewIfNeeded();
    await page.screenshot({ path: 'screenshots/i-running.png' });

    await expect(prog).toHaveAttribute('data-phase', 'done', SLOW);
    await expect(prog.getByTestId('upgrade-progress-done')).toContainText(`Upgraded to ${PIN}`);
    await expect(prog.locator('[data-state="done"]')).toHaveCount(6);
    await expect(prog.getByTestId('verify-sha')).toContainText('matches');
    await expect(prog.getByTestId('verify-selftest')).toContainText(PIN);
    await expect(app.getByTestId('agent-installed')).toHaveText(PIN);
    await expect(app.getByTestId('upgrade-badge')).toHaveText('Up to date');
    await expect(app.getByTestId('upgrade-agent')).toHaveCount(0);
    await app.scrollIntoViewIfNeeded();
    await page.screenshot({ path: 'screenshots/i-done.png' });

    // activities: site agent.upgrade-started / agent.upgraded + cloud agent.upgrade-requested; audit entry
    await page.goto(`${E}/activities?type=agent.`);
    await expect(page.getByTestId('activity-row').filter({ hasText: 'agent.upgraded' }).first()).toBeVisible();
    await expect(page.getByTestId('activity-row').filter({ hasText: 'agent.upgrade-requested' }).first()).toBeVisible();
    await page.getByTestId('activity-row').filter({ hasText: 'agent.upgraded' }).first().click();
    await expect(page.getByTestId('activity-agent-fromto')).toHaveText(`0.10.0-10080900 → ${PIN}`);
    await expect(page.getByTestId('activity-agent')).toContainText('cloud console');
    await page.goto('/app/audit-log?action=deployment.agent-upgrade-requested');
    const row = page.getByTestId('audit-row').first();
    await expect(row).toHaveAttribute('data-action', 'deployment.agent-upgrade-requested');
    await row.click();
    await expect(page.getByTestId('audit-drawer-action-label')).toHaveText('Agent upgrade requested');
  });

  test('cancel a queued request (agent has not picked it up)', async ({ page }) => {
    await login(page, 'admin', `${E}/setup`);
    await agent(page, 'app', { _upgHold: true });
    await requestUpgrade(page, 'app');
    const app = page.getByTestId('setup-site-app');
    await expect(app.getByTestId('upgrade-queued')).toBeVisible();
    await app.getByTestId('upgrade-cancel').click();
    await expectToast(page, 'success', 'Upgrade of app cancelled');
    await expect(app.getByTestId('upgrade-queued')).toHaveCount(0);
    await expect(app.getByTestId('upgrade-badge')).toHaveAttribute('data-state', 'available');
    await expect(app.getByTestId('upgrade-agent')).toBeEnabled();
    await page.goto('/app/audit-log?action=deployment.agent-upgrade-cancelled');
    const row = page.getByTestId('audit-row').first();
    await expect(row).toHaveAttribute('data-action', 'deployment.agent-upgrade-cancelled');
    await row.click();
    await expect(page.getByTestId('audit-drawer-action-label')).toHaveText('Agent upgrade cancelled');
  });

  test('failure in verify is shown with the verification result; Try again is offered', async ({ page }) => {
    await login(page, 'admin', `${E}/setup`);
    await agent(page, 'app', { _upgFail: 'verify' });
    await requestUpgrade(page, 'app');
    const app = page.getByTestId('setup-site-app');
    const prog = app.getByTestId('upgrade-progress');
    await expect(prog).toHaveAttribute('data-phase', 'failed', SLOW);
    await expect(prog.getByTestId('upgrade-progress-failed')).toContainText('Upgrade failed — still on 0.10.0-10080900');
    await expect(prog.getByTestId('upgrade-progress-failed')).toContainText('release signature does not verify');
    await expect(prog.locator('[data-step="verify"]')).toHaveAttribute('data-state', 'failed');
    await expect(prog.locator('[data-step="download"]')).toHaveAttribute('data-state', 'done');
    await expect(prog.getByTestId('verify-signature')).toContainText('invalid');
    await expect(app.getByTestId('upgrade-agent')).toHaveText('Try again');
    await expect(app.getByTestId('agent-installed')).toHaveText('0.10.0-10080900');
    await app.scrollIntoViewIfNeeded();
    await page.screenshot({ path: 'screenshots/i-failed.png' });
  });

  test('not healthy in time → rolled back is shown clearly', async ({ page }) => {
    await login(page, 'admin', `${E}/setup`);
    await agent(page, 'app', { _upgFail: 'health' });
    await requestUpgrade(page, 'app');
    const app = page.getByTestId('setup-site-app');
    const prog = app.getByTestId('upgrade-progress');
    await expect(prog).toHaveAttribute('data-phase', 'rolled-back', SLOW);
    await expect(prog.getByTestId('upgrade-progress-rolled-back')).toContainText('Rolled back to 0.10.0-10080900');
    await expect(prog.getByTestId('upgrade-progress-rolled-back')).toContainText('did not report healthy within 60 s');
    await expect(prog.locator('[data-step="health"]')).toHaveAttribute('data-state', 'failed');
    await expect(app.getByTestId('upgrade-badge')).toHaveAttribute('data-state', 'available');
    await app.scrollIntoViewIfNeeded();
    await page.screenshot({ path: 'screenshots/i-rolled-back.png' });
    await page.goto(`${E}/activities?type=agent.rolled-back`);
    await page.getByTestId('activity-row').first().click();
    await expect(page.getByTestId('activity-agent-fromto')).toHaveText(`${PIN} → 0.10.0-10080900`);
    await expect(page.getByTestId('activity-agent')).toContainText('upgrade watcher');
  });

  test('upgrade all: order + skipped site, typed UPGRADE, sequential to done', async ({ page }) => {
    await login(page, 'admin', `${E}/setup`);
    await agent(page, 'lab', { agentVersion: '0.10.0-10081000' });
    await page.reload();
    const rel = page.getByTestId('agent-release-panel');
    await expect(rel.getByTestId('below-count')).toContainText('3 of 3 sites are below it (1 needs the install command once)');
    await rel.getByTestId('upgrade-all').click();
    const dlg = page.getByTestId('upgrade-all-confirm');
    await expect(dlg.getByTestId('order-site')).toHaveCount(2);
    await expect(dlg.getByTestId('order-site').nth(0)).toHaveAttribute('data-site', 'app');
    await expect(dlg.getByTestId('order-site').nth(1)).toHaveAttribute('data-site', 'lab');
    await expect(dlg.getByTestId('skipped-site')).toHaveCount(1);
    await expect(dlg.getByTestId('skipped-site')).toContainText('db');
    await expect(dlg.getByTestId('skipped-site')).toContainText('Rerun the install command');
    await expect(dlg.getByTestId('confirm-ok')).toBeDisabled();
    await dlg.getByTestId('typed-confirm').fill('UPGRADE');
    await page.screenshot({ path: 'screenshots/i-upgrade-all-dialog.png' });
    await dlg.getByTestId('confirm-ok').click();
    await expectToast(page, 'success', 'Upgrade all started');

    const run = rel.getByTestId('upgrade-run');
    await expect(run).toHaveAttribute('data-status', 'running');
    const site = (s: string) => run.locator(`[data-testid="run-site"][data-site="${s}"]`);
    await expect(site('db')).toHaveAttribute('data-status', 'skipped');
    // sequential: lab waits while app is upgrading
    await expect(site('app')).toHaveAttribute('data-status', /requested|running/);
    await expect(site('lab')).toHaveAttribute('data-status', 'pending');
    await expect(site('app').getByTestId('run-progress')).toBeVisible(SLOW);
    await expect(page.getByTestId('setup-site-lab').getByTestId('upgrade-agent')).toBeDisabled();
    await page.screenshot({ path: 'screenshots/i-upgrade-all-running.png' });
    await expect(site('app')).toHaveAttribute('data-status', 'done', SLOW);
    await expect(site('lab')).toHaveAttribute('data-status', /requested|running|done/, SLOW);
    await expect(site('lab')).toHaveAttribute('data-status', 'done', SLOW);
    await expect(run).toHaveAttribute('data-status', 'done');
    await expect(run.getByTestId('run-status')).toHaveText('done');
    await expect(rel.getByTestId('below-count')).toContainText('1 of 3 sites is below it');
    await page.screenshot({ path: 'screenshots/i-upgrade-all-done.png', fullPage: true });
  });

  test('upgrade all stops on the first failure; cancel run', async ({ page }) => {
    await login(page, 'admin', `${E}/setup`);
    await agent(page, 'lab', { agentVersion: '0.10.0-10081000' });
    await agent(page, 'app', { _upgFail: 'download' });
    await page.reload();
    const rel = page.getByTestId('agent-release-panel');
    await rel.getByTestId('upgrade-all').click();
    const dlg = page.getByTestId('upgrade-all-confirm');
    await dlg.getByTestId('typed-confirm').fill('UPGRADE');
    await dlg.getByTestId('confirm-ok').click();
    const run = rel.getByTestId('upgrade-run');
    const site = (s: string) => run.locator(`[data-testid="run-site"][data-site="${s}"]`);
    await expect(run).toHaveAttribute('data-status', 'failed', SLOW);
    await expect(site('app')).toHaveAttribute('data-status', 'failed');
    await expect(site('app').getByTestId('run-site-error')).toContainText('connection reset');
    await expect(site('lab')).toHaveAttribute('data-status', 'cancelled');
    await expect(run).toContainText('The run stopped at the first failure');
    await page.screenshot({ path: 'screenshots/i-upgrade-all-failed.png', fullPage: true });

    // a new run, cancelled while app waits in the queue
    await agent(page, 'app', { _upgFail: null, _upgHold: true });
    await rel.getByTestId('upgrade-all').click();
    await dlg.getByTestId('typed-confirm').fill('UPGRADE');
    await dlg.getByTestId('confirm-ok').click();
    await expect(run).toHaveAttribute('data-status', 'running');
    await expect(site('app')).toHaveAttribute('data-status', 'requested');
    await expect(page.getByTestId('setup-site-app').getByTestId('upgrade-cancel')).toHaveCount(0); // part of the run
    await rel.getByTestId('run-cancel').click();
    await page.getByTestId('run-cancel-confirm').getByTestId('confirm-ok').click();
    await expect(run).toHaveAttribute('data-status', 'cancelled');
    await expect(site('app')).toHaveAttribute('data-status', 'cancelled');
    await expect(site('lab')).toHaveAttribute('data-status', 'cancelled');
    await expect(page.getByTestId('setup-site-app').getByTestId('upgrade-queued')).toHaveCount(0);
  });

  test('agent below 0.10.0: disabled button, hint, upgrade path (CLI + the upgrade command of the site inline)', async ({ page }) => {
    await login(page, 'admin', `${E}/setup`);
    const db = page.getByTestId('setup-site-db');
    await expect(db.getByTestId('agent-installed')).toHaveText('0.9.0-10080530');
    await expect(db.getByTestId('upgrade-agent')).toBeDisabled();
    await expect(db.getByTestId('upgrade-hint')).toContainText('Rerun the install command of this site to reach agent ≥ 0.10.0');
    const path = db.getByTestId('upgrade-path');
    await expect(path.getByTestId('upgrade-cli')).toHaveText('sudo opm-agent upgrade');
    // §21 #92 the site's upgrade command sits inline in the path (db: token from before commands were kept → Regenerate)
    await expect(path.getByTestId('upgrade-rerun')).toContainText("Run this site's upgrade command on the server");
    await expect(path.getByTestId('upgrade-command')).toHaveAttribute('data-stored', '0');
    await expect(path.getByTestId('upgrade-command-history')).toContainText('v1/install/script');
    await expect(db.getByTestId('token-upgrade')).toHaveText('upgrade command');
    // §21 #91 the install command is the hard fallback for any up- / downgrade
    const fb = db.getByTestId('upgrade-fallback');
    await fb.locator('summary').click();
    await expect(fb).toContainText('UPGRADE / DOWNGRADE / SAME');
    await expect(fb).toContainText('OPM_AGENT_VERSION=');
    await expect(fb).toContainText('OPM_ALLOW_DOWNGRADE=1');
    await db.getByTestId('upgrade-path-link').click();
    await expect(page).toHaveURL(/#upgrade-path-db$/);
    await expect(path).toBeInViewport();
    await page.screenshot({ path: 'screenshots/i-old-agent.png' });
    // the API refuses too (details.hint)
    const r = await page.evaluate(async (dep) => {
      const res = await (window as unknown as { __opmMockFetch: typeof fetch }).__opmMockFetch(`${location.origin}/v1/deployments/${dep}/agents/db/upgrade`, { method: 'POST', headers: { Authorization: 'Bearer mock:uAdmin00000000000001', 'Content-Type': 'application/json' }, body: JSON.stringify({ version: '0.10.0-10081200', confirm: 'db' }) });
      return { status: res.status, body: await res.json() };
    }, SEED.deployment);
    expect(r.status).toBe(422);
    expect(r.body.error.details.hint).toContain('Rerun the install command');
  });

  test('status page: agent version + upgrade badge per site, link to the Setup card', async ({ page }) => {
    await login(page, 'admin', `${E}/status`);
    const row = (s: string) => page.getByTestId(`status-row-${s}`);
    await expect(row('app').getByTestId('status-agent')).toContainText('v0.10.0-10080900');
    await expect(row('app').getByTestId('upgrade-badge')).toHaveText('Upgrade available');
    await expect(row('lab').getByTestId('upgrade-badge')).toHaveText('Up to date');
    await expect(row('db').getByTestId('upgrade-badge')).toHaveAttribute('data-state', 'available');
    await page.screenshot({ path: 'screenshots/i-status.png' });
    await row('db').getByTestId('status-upgrade-link').click();
    await expect(page).toHaveURL(/\/setup\?site=db$/);
    await expect(page.getByTestId('setup-site-db').getByTestId('upgrade-path')).toBeInViewport();
  });

  test('activities drawer: agent settings change, support bundle, failed + rolled back (seeded)', async ({ page }) => {
    await login(page, 'admin', `${E}/activities?site=app&type=agent.`);
    await expect(page.getByTestId('activity-row').first()).toBeVisible();
    await expect(page.getByTestId('activity-icon').first()).toBeVisible();
    await page.getByTestId('activity-row').filter({ hasText: 'agent.config-changed' }).click();
    await expect(page.getByTestId('activity-agent-changes')).toContainText('tools.hostShell');
    await expect(page.getByTestId('activity-agent-changes')).toContainText('true');
    await page.screenshot({ path: 'screenshots/i-activity-config.png' });
    await page.keyboard.press('Escape');
    await page.getByTestId('activity-row').filter({ hasText: 'agent.upgrade-failed' }).click();
    await expect(page.getByTestId('activity-agent-phase')).toHaveText('health');
    await expect(page.getByTestId('activity-agent-error')).toContainText('did not report healthy');
    await expect(page.getByTestId('activity-drawer')).toContainText('Agent upgrade failed');
    await page.keyboard.press('Escape');
    await page.getByTestId('activity-row').filter({ hasText: 'agent.support-bundle-downloaded' }).click();
    await expect(page.getByTestId('activity-agent')).toContainText('182 KiB');
    await expect(page.getByTestId('activity-agent')).toContainText('agent.json');
  });

  test('phone width (390): setup cards + run panel fit without horizontal scroll', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await login(page, 'admin', `${E}/setup`);
    await agent(page, 'app', { _upgHold: true });
    await requestUpgrade(page, 'app');
    await expect(page.getByTestId('setup-site-app').getByTestId('upgrade-queued')).toBeVisible();
    const sw = await page.evaluate(() => document.documentElement.scrollWidth);
    expect(sw).toBeLessThanOrEqual(390);
    await page.screenshot({ path: 'screenshots/i-mobile-setup.png', fullPage: true });
    await agent(page, 'app', { _upgHold: false });
    await expect(page.getByTestId('setup-site-app').getByTestId('upgrade-progress')).toBeVisible(SLOW);
    await page.getByTestId('setup-site-app').getByTestId('upgrade-progress').scrollIntoViewIfNeeded();
    await page.screenshot({ path: 'screenshots/i-mobile-running.png' });
    await page.getByTestId('setup-site-db').getByTestId('upgrade-path').scrollIntoViewIfNeeded();
    await page.screenshot({ path: 'screenshots/i-mobile-old-agent.png' });
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
  });
});
