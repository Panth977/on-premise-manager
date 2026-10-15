import { test, expect } from '@playwright/test';
import { login, SEED } from './helpers';

/**
 * Round H (SPEC-final §21 #86): the Setup page lists the agent GUI URLs each enrolled site reported in its heartbeat
 * (port 80 first), the default-edge note, edge port alerts, a fallback for agents < 0.8 and the upgrade / firewall help.
 * Mock seed: app = default edge on :80, db = :80 held by nginx.
 */
const D = `/app/deployments/${SEED.deployment}/setup`;

test.describe('Setup · agent GUI URLs + always-on edge', () => {
  test('URLs from the heartbeat, port 80 first; default edge note; port alert; help text', async ({ page }) => {
    await login(page, 'admin', D);
    const app = page.getByTestId('setup-site-app');
    const urls = app.getByTestId('gui-url');
    await expect(urls).toHaveCount(3);
    await expect(urls.nth(0).locator('a')).toHaveText(/^http:\/\/10\.0\.0\.11\/_opm\/\s*$/);
    await expect(urls.nth(0).locator('a')).toHaveAttribute('href', 'http://10.0.0.11/_opm/');
    await expect(urls.nth(0).locator('a')).toHaveAttribute('target', '_blank');
    await expect(urls.nth(1).locator('a')).toHaveAttribute('href', 'http://10.0.0.11:8090/_opm/');
    await expect(urls.nth(2).locator('a')).toHaveAttribute('href', 'http://10.0.0.11:7080/_opm/');
    await expect(urls.nth(0)).toContainText('edge');
    await expect(app.getByTestId('default-edge')).toHaveText('Port 80: default edge (no configuration applied yet) — GUI at /_opm/');
    await expect(app.getByTestId('port-alert')).toHaveCount(0);
    await expect(app.getByTestId('gui-urls-fallback')).toHaveCount(0);

    const db = page.getByTestId('setup-site-db');
    await expect(db.getByTestId('port-alert')).toHaveCount(1);
    await expect(db.getByTestId('port-alert')).toHaveText('port 80 is used by nginx — stop it or change edge.httpPort');
    await expect(db.getByTestId('default-edge')).toHaveCount(0);
    await expect(db.getByTestId('gui-url').first().locator('a')).toHaveAttribute('href', 'http://10.0.0.12:8091/_opm/');

    const help = page.getByTestId('install-help');
    await expect(help).toContainText("Upgrade an installed site: agent 0.10+ → Upgrade agent on its card below. Older agents, once: run the site's Upgrade / reinstall command (on its card — always viewable, or Regenerate) on that server");
    await expect(help).toContainText('sudo opm-agent upgrade');
    await expect(help).toContainText('DigitalOcean Cloud Firewall');
    await expect(page.locator('main, body').first()).not.toContainText(/takeover/i);

    await page.screenshot({ path: 'screenshots/setup-guiurls-light.png', fullPage: true });
    await page.emulateMedia({ colorScheme: 'dark' });
    await page.screenshot({ path: 'screenshots/setup-guiurls-dark.png', fullPage: true });
  });

  test('old agent (no guiUrls reported) → default ports on the first IPv4, labelled', async ({ page }) => {
    await login(page, 'admin', D);
    await expect(page.getByTestId('setup-site-app').getByTestId('gui-url')).toHaveCount(3);
    // let the mock db persist first (debounced save), then drop the 0.8 heartbeat fields
    await page.waitForTimeout(400);
    await page.evaluate((dep) => {
      const db = JSON.parse(localStorage.getItem('opm.mock.db')!);
      const a = db.agents[dep].app;
      delete a.guiPorts;
      delete a.edge.defaultEdge;
      localStorage.setItem('opm.mock.db', JSON.stringify(db));
    }, SEED.deployment);
    await page.reload();
    const app = page.getByTestId('setup-site-app');
    const urls = app.getByTestId('gui-url');
    await expect(urls).toHaveCount(2);
    await expect(urls.nth(0).locator('a')).toHaveAttribute('href', 'http://10.0.0.11:8080/_opm/');
    await expect(urls.nth(1).locator('a')).toHaveAttribute('href', 'http://10.0.0.11:7080/_opm/');
    await expect(app.getByTestId('gui-urls-fallback')).toHaveText('(default ports — agent did not report its listeners)');
    await expect(app.getByTestId('default-edge')).toHaveCount(0);
    await page.screenshot({ path: 'screenshots/setup-guiurls-old-agent.png', fullPage: true });
  });
});

test('activities: update.failed shows the failed containers with the log tail (§21 #87)', async ({ page }) => {
  await login(page, 'admin', `/app/deployments/${SEED.deployment}/activities?type=update.failed`);
  await page.getByTestId('activity-row').filter({ hasText: 'hello-api' }).first().click();
  await expect(page.getByTestId('activity-failure')).toContainText('exit code 3');
  await expect(page.getByTestId('activity-failure')).toContainText('fatal: DB_URL is not set');
  await expect(page.getByTestId('activity-failure')).toContainText('docker logs opm_dep_app_hello-api-failed-1791400000');
});
